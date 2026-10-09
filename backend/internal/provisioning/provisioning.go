// Package provisioning answers which database a participant works in: copies
// of a contest's template are made early and kept spare, so claiming one is a
// row update. It also owns how a game is described (a script, an uploaded
// dump, or a structural definition with CSV rows).
//
// It does not run participants' SQL (internal/queryrunner, internal/sqlpolicy)
// or connect to the game cluster itself (internal/gamedb, behind Cluster).
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

var (
	// ErrNoSpare means the pool was empty, not that anything went wrong.
	ErrNoSpare = errors.New("no spare copy")
	// ErrNoInstance means this registration has no database yet.
	ErrNoInstance = errors.New("no instance")
	// ErrNoGame means the contest's template was never built, or is still
	// building.
	ErrNoGame = errors.New("no game database")
	// ErrClusterFull means the game cluster has no room left within
	// GAME_CLUSTER_MAX_BYTES for another copy of this contest's template. It is
	// temporary and must not read as an internal error (CLAUDE.md rule 1).
	ErrClusterFull = errors.New("the game cluster has no room for another copy")
)

// Instance is the database a registration works in.
type Instance struct {
	Database        string
	TemplateVersion int
	Status          string
}

// InstanceStatusDropped is the terminal status Reclaim leaves on a row once
// its database is gone from the cluster. Such a row is history, never a
// database Ensure may hand back.
const InstanceStatusDropped = "dropped"

// Stale is a database left over from an older template.
type Stale struct {
	Database string
	// Registration is nil for a spare copy nobody had claimed.
	Registration *uuid.UUID
}

// Repository is the record of which database belongs to whom.
type Repository interface {
	ClaimSpare(ctx context.Context, contest, registration uuid.UUID, version int) (string, error)
	AddSpare(ctx context.Context, contest uuid.UUID, database string, version int) error
	Assign(ctx context.Context, contest, registration uuid.UUID, database string, version int) error
	Of(ctx context.Context, registration uuid.UUID) (Instance, error)
	Stale(ctx context.Context, contest uuid.UUID, version int) ([]Stale, error)
	Forget(ctx context.Context, database string) error
	SpareCount(ctx context.Context, contest uuid.UUID, version int) (int, error)
	// WaitingParticipants counts the contest's registrations that hold no
	// current copy; the pool's depth is sized from it.
	WaitingParticipants(ctx context.Context, contest uuid.UUID, version int) (int, error)
	AllCurrent(ctx context.Context, contest uuid.UUID, version int) (bool, error)
	// Live lists the published or running contests whose template finished
	// building.
	Live(ctx context.Context) ([]Contest, error)
	// Game returns one contest's game, or ErrNoGame.
	Game(ctx context.Context, contestID uuid.UUID) (Contest, error)
	// Reclaimable lists up to limit not-yet-dropped instances of contests
	// finished or archived longer ago than their grace period (or
	// installationGraceMin when they set none).
	Reclaimable(ctx context.Context, installationGraceMin, limit int) ([]ReclaimCandidate, error)
	// MarkDropped moves one instance to the terminal 'dropped' status. The row
	// stays so the audit trail still has something to point to.
	MarkDropped(ctx context.Context, database string) error
	// ReclaimableTemplates lists up to limit 'ready' templates of contests
	// past their grace whose instances are all gone. A template is never
	// offered ahead of the instances copied from it.
	ReclaimableTemplates(ctx context.Context, installationGraceMin, limit int) ([]TemplateCandidate, error)
	// MarkTemplateDropped moves one contest's template to the terminal
	// 'dropped' status.
	MarkTemplateDropped(ctx context.Context, contestID uuid.UUID) error
	// Instances lists up to limit of one contest's rows, dropped ones
	// included, newest last (CLAUDE.md rule 2).
	Instances(ctx context.Context, contest uuid.UUID, limit int) ([]InstanceRecord, error)
	// InstanceNamed reads one row of a contest by database name, or
	// ErrInstanceNotFound. Scoped to the contest because db_name is unique
	// across the installation, so a name alone would reach another contest.
	InstanceNamed(ctx context.Context, contest uuid.UUID, database string) (InstanceRecord, error)
}

// Cluster is the part of the game cluster this service drives. It is kept
// narrow so a schema per participant could replace a database per participant
// without changing the service.
type Cluster interface {
	CreateInstance(ctx context.Context, template, instance string, policy sqlpolicy.Policy) error
	Drop(ctx context.Context, name string) error
	DatabaseSize(ctx context.Context, name string) (int64, error)
	// ClusterBytes is how much disk every database on the cluster occupies
	// together.
	ClusterBytes(ctx context.Context) (int64, error)
	// DatabaseSizes is DatabaseSize for a whole list in one round trip. Names
	// that no longer exist are absent from the result rather than an error.
	DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error)
	// DropIdle removes name only if nobody is connected to it, and reports
	// whether it did. Unlike Drop it never forces a connection closed: the
	// reclaim sweep cannot tell a forgotten session from a running query.
	DropIdle(ctx context.Context, name string) (dropped bool, err error)
}

// Contest is what the service needs to know about one olympiad.
type Contest struct {
	ID       uuid.UUID
	Template string
	Version  int
	Policy   sqlpolicy.Policy
}

// Service provisions databases.
type Service struct {
	repo    Repository
	cluster Cluster
	workers int
	// audit and uow are set by WithAudit; nil means Reclaim records nothing.
	audit *audit.Recorder
	uow   storage.UnitOfWork
	// maxClusterBytes is a copy of PoolLimits.MaxClusterBytes, so the budget
	// also binds on the participant's own path (roomForOneCopy). Zero means no
	// budget.
	maxClusterBytes int64
	sizes           templateSizes
}

// DefaultWorkers is how many copies are made at once: enough to fill a pool in
// reasonable time, few enough that filling it never busies the cluster.
const DefaultWorkers = 3

// ReclaimBatchLimit bounds how many instances (and, separately, how many
// templates) one Reclaim pass drops, so a backlog of long-finished contests
// does not become thousands of DROP DATABASE statements in one tick. At one
// pass every ten minutes, a few thousand instances still clear within a day.
const ReclaimBatchLimit = 200

// New assembles the service.
func New(repo Repository, cluster Cluster) *Service {
	return &Service{repo: repo, cluster: cluster, workers: DefaultWorkers}
}

// WithWorkers sets how many copies may be made at once.
func (s *Service) WithWorkers(workers int) *Service {
	if workers < 1 {
		workers = 1
	}
	s.workers = workers
	return s
}

// WithAudit lets Reclaim record what it drops, in the same transaction that
// marks the row dropped. Only tests leave it unset.
func (s *Service) WithAudit(rec *audit.Recorder, uow storage.UnitOfWork) *Service {
	s.audit = rec
	s.uow = uow
	return s
}

// WithClusterBudget sets how much disk every database on the game cluster may
// occupy together. It must be the same GAME_CLUSTER_MAX_BYTES given to
// PoolLimits.MaxClusterBytes; zero means no budget.
func (s *Service) WithClusterBudget(maxBytes int64) *Service {
	s.maxClusterBytes = maxBytes
	return s
}

// Ensure returns the database this registration works in, making one if there
// is none. Cheapest first: an existing current copy, then a spare, and only
// with an empty pool a CREATE DATABASE while the participant waits.
func (s *Service) Ensure(ctx context.Context, contest Contest, registration uuid.UUID) (string, error) {
	existing, err := s.Instance(ctx, registration)
	return s.EnsureFrom(ctx, contest, registration, existing, err)
}

// Instance returns the database a registration already has, or
// ErrNoInstance.
func (s *Service) Instance(ctx context.Context, registration uuid.UUID) (Instance, error) {
	return s.repo.Of(ctx, registration)
}

// EnsureFrom is Ensure for a caller that has already read the registration's
// instance: existing and err are what Instance answered, ErrNoInstance
// included.
func (s *Service) EnsureFrom(ctx context.Context, contest Contest, registration uuid.UUID, existing Instance, err error) (string, error) {
	switch {
	case err == nil && existing.Status != InstanceStatusDropped && existing.TemplateVersion >= contest.Version:
		return existing.Database, nil
	case err == nil && existing.Status != InstanceStatusDropped:
		// From an older template: rebuilt now rather than by the sweep, so the
		// first to arrive does not play on old data.
		return s.rebuildExisting(ctx, contest, registration, existing.Database)
	case err == nil:
		// Dropped: the row is history and its database is gone, whatever
		// version it carries.
		return s.rebuildExisting(ctx, contest, registration, existing.Database)
	case !errors.Is(err, ErrNoInstance):
		return "", err
	}

	switch database, err := s.repo.ClaimSpare(ctx, contest.ID, registration, contest.Version); {
	case err == nil:
		return database, nil
	case !errors.Is(err, ErrNoSpare):
		return "", err
	}

	// The pool was empty: the one path where somebody waits for a copy, and
	// where the byte budget must still be checked.
	if err := s.roomForOneCopy(ctx, contest); err != nil {
		return "", err
	}

	database := instanceName(contest.ID, registration)
	if err := s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy); err != nil {
		return "", err
	}
	if err := s.repo.Assign(ctx, contest.ID, registration, database, contest.Version); err != nil {
		// Nothing points at the new database; drop it so a retry does not
		// collide with it.
		_ = s.cluster.Drop(context.WithoutCancel(ctx), database)
		return "", err
	}
	return database, nil
}

// TopUp brings the pool up to depth, and reports how many copies it made.
// Copies are made by s.workers at a time, never in a burst: CREATE DATABASE
// is heavy enough to slow the whole cluster.
func (s *Service) TopUp(ctx context.Context, contest Contest, depth int) (int, error) {
	have, err := s.repo.SpareCount(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, err
	}
	want := depth - have
	if want <= 0 {
		return 0, nil
	}

	work := make(chan struct{}, want)
	for range want {
		work <- struct{}{}
	}
	close(work)

	var (
		mu     sync.Mutex
		made   int
		failed error
		party  sync.WaitGroup
	)

	for range min(s.workers, want) {
		party.Add(1)
		go func() {
			defer party.Done()
			for range work {
				if ctx.Err() != nil {
					return
				}
				if err := s.addSpare(ctx, contest); err != nil {
					mu.Lock()
					failed = errors.Join(failed, err)
					mu.Unlock()
					return
				}
				mu.Lock()
				made++
				mu.Unlock()
			}
		}()
	}
	party.Wait()

	return made, failed
}

func (s *Service) addSpare(ctx context.Context, contest Contest) error {
	database := spareName(contest.ID)
	if err := s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy); err != nil {
		return err
	}
	if err := s.repo.AddSpare(ctx, contest.ID, database, contest.Version); err != nil {
		// Nothing points at the copy; drop it so it does not leak.
		_ = s.cluster.Drop(context.WithoutCancel(ctx), database)
		return err
	}
	return nil
}

// Invalidate removes every database of this contest made from an older
// template, claimed or spare, and reports how many.
func (s *Service) Invalidate(ctx context.Context, contest Contest) (int, error) {
	stale, err := s.repo.Stale(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, err
	}

	if len(stale) > 0 {
		// The template was rebuilt, so its old measurement is tidied away.
		// Only when something was stale, so an unchanged contest keeps it.
		s.sizes.forget(contest.Template)
	}

	dropped := 0
	for _, old := range stale {
		if err := s.cluster.Drop(ctx, old.Database); err != nil {
			return dropped, err
		}
		if err := s.repo.Forget(ctx, old.Database); err != nil {
			return dropped, err
		}
		dropped++
	}
	return dropped, nil
}

// Reset gives a participant a fresh copy of their starting database under the
// same name.
func (s *Service) Reset(ctx context.Context, contest Contest, registration uuid.UUID) error {
	existing, err := s.repo.Of(ctx, registration)
	if err != nil {
		return err
	}
	if err := s.replace(ctx, contest, existing.Database); err != nil {
		return err
	}
	return s.repo.Assign(ctx, contest.ID, registration, existing.Database, contest.Version)
}

// Depth answers how many spare copies one contest should keep ready. It is a
// function of the roster rather than a fixed number, since everybody past a
// fixed depth waits for CREATE DATABASE at the moment they all arrive.
type Depth func(ctx context.Context, contest Contest) (int, error)

// Tend keeps every live contest's pool where it should be: stale copies gone,
// depth topped up. It reports how many databases it made and removed. A failing
// contest is reported and the others are still tended.
func (s *Service) Tend(ctx context.Context, depth Depth) (made, dropped int, err error) {
	contests, err := s.repo.Live(ctx)
	if err != nil {
		return 0, 0, err
	}

	var failures []error
	for _, contest := range contests {
		// Invalidate first: a stale copy would otherwise count toward the
		// depth and keep TopUp from replacing it.
		removed, err := s.Invalidate(ctx, contest)
		dropped += removed
		if err != nil {
			failures = append(failures, fmt.Errorf("contest %s: %w", contest.ID, err))
			continue
		}

		want, err := depth(ctx, contest)
		if err != nil {
			failures = append(failures, fmt.Errorf("contest %s: %w", contest.ID, err))
			continue
		}

		added, err := s.TopUp(ctx, contest, want)
		made += added
		if err != nil {
			failures = append(failures, fmt.Errorf("contest %s: %w", contest.ID, err))
		}
	}
	return made, dropped, errors.Join(failures...)
}

// Quota is how large one participant's database may grow: a multiple of the
// template's size, so it scales with the contest's data. It runs on every query
// of a read-write contest, hence the cached measurement (templateSizes).
func (s *Service) Quota(ctx context.Context, contest Contest) (int64, error) {
	size, err := s.templateBytes(ctx, contest)
	if err != nil {
		return 0, err
	}

	ratio := contest.Policy.DiskQuotaRatio
	if ratio <= 0 {
		ratio = sqlpolicy.DefaultDiskQuotaRatio
	}
	// A floor, so a tiny template does not yield a quota one INSERT exhausts.
	const smallest = 16 << 20
	if quota := size * int64(ratio); quota > smallest {
		return quota, nil
	}
	return smallest, nil
}

// Ready reports whether the contest may start: every database it has must have
// come from the current template.
func (s *Service) Ready(ctx context.Context, contest Contest) (bool, error) {
	return s.repo.AllCurrent(ctx, contest.ID, contest.Version)
}

// replace makes a database new again, keeping its name.
func (s *Service) replace(ctx context.Context, contest Contest, database string) error {
	return s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy)
}

// rebuildExisting makes database new again under its existing name and
// reassigns it to registration at the current version.
func (s *Service) rebuildExisting(ctx context.Context, contest Contest, registration uuid.UUID, database string) (string, error) {
	if err := s.replace(ctx, contest, database); err != nil {
		return "", err
	}
	if err := s.repo.Assign(ctx, contest.ID, registration, database, contest.Version); err != nil {
		return "", err
	}
	return database, nil
}

// Names use 12 hex characters of each identifier: unique enough for one
// installation, within PostgreSQL's 63 characters, and still showing which
// contest a stray database belongs to.
func instanceName(contest, registration uuid.UUID) string {
	return "game_c" + short(contest) + "_u" + short(registration)
}

func spareName(contest uuid.UUID) string {
	return "game_pool_c" + short(contest) + "_" + short(uuid.New())
}

func short(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:12]
}

// Databases reports how many spare copies the contest's pool holds.
func (s *Service) Databases(ctx context.Context, contest Contest) (spare int, err error) {
	spare, err = s.repo.SpareCount(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, fmt.Errorf("read the pool depth: %w", err)
	}
	return spare, nil
}

// PoolLimits is everything that bounds one contest's pool of spare copies.
type PoolLimits struct {
	// Headroom is how many copies to keep beyond the participants who hold
	// none, so a late self-enrolment does not wait.
	Headroom int
	// MaxCopies caps how many copies one contest may ask for. Zero means no
	// cap. It does not bound disk on its own: the cost depends on the
	// template's size.
	MaxCopies int
	// MaxClusterBytes is how much disk every database on the game cluster may
	// occupy together (GAME_CLUSTER_MAX_BYTES). Zero means no budget. It is
	// per cluster because the cluster is what runs out, and it is configured
	// because PostgreSQL does not portably report free disk space.
	MaxClusterBytes int64
}

// PoolBound names what stopped a pool being as deep as its roster asked.
type PoolBound string

const (
	// BoundNone is a pool that got what it asked for.
	BoundNone PoolBound = ""
	// BoundCopies is PoolLimits.MaxCopies.
	BoundCopies PoolBound = "copies"
	// BoundDisk is PoolLimits.MaxClusterBytes.
	BoundDisk PoolBound = "disk"
)

// Sizing is one contest's pool depth and the measurements behind it, so a
// constrained pool can say why it stopped growing.
type Sizing struct {
	// Depth is what the pool is allowed to be.
	Depth int
	// Wanted is what the roster asked for, before any bound applied.
	Wanted int
	// Bound is what cut it, BoundNone when nothing did.
	Bound PoolBound
	// TemplateBytes is what one copy of this contest costs, and ClusterBytes
	// what the cluster already holds. Both zero when the byte budget was not
	// consulted.
	TemplateBytes int64
	ClusterBytes  int64
	// Budget is the MaxClusterBytes the two above were measured against.
	Budget int64
}

// Constrained is told whenever a pool was granted less than its roster asked
// for. A callback because this domain package has no logger; the composition
// root reports it.
type Constrained func(ctx context.Context, contest Contest, sizing Sizing)

// RosterDepth is the depth a contest's roster asks for: everybody without a
// current copy plus headroom, cut back to MaxCopies and to what fits in
// MaxClusterBytes. Self-enrolment lets outsiders grow the roster, so the byte
// budget is what keeps it from filling the shared disk. constrained may be nil.
func (s *Service) RosterDepth(limits PoolLimits, constrained Constrained) Depth {
	return func(ctx context.Context, contest Contest) (int, error) {
		waiting, err := s.repo.WaitingParticipants(ctx, contest.ID, contest.Version)
		if err != nil {
			return 0, fmt.Errorf("size the pool for contest %s: %w", contest.ID, err)
		}

		sizing := Sizing{Wanted: waiting + limits.Headroom}
		sizing.Depth = sizing.Wanted
		if limits.MaxCopies > 0 && sizing.Depth > limits.MaxCopies {
			sizing.Depth, sizing.Bound = limits.MaxCopies, BoundCopies
		}

		// Measured only when a byte budget is set, and even under the count
		// cap, because the count is not what fills a disk.
		if limits.MaxClusterBytes > 0 {
			fits, err := s.copiesThatFit(ctx, contest, limits.MaxClusterBytes, &sizing)
			if err != nil {
				return 0, err
			}
			if sizing.Depth > fits {
				sizing.Depth, sizing.Bound = fits, BoundDisk
			}
		}

		if sizing.Bound != BoundNone && constrained != nil {
			constrained(ctx, contest, sizing)
		}
		return sizing.Depth, nil
	}
}

// copiesThatFit is how deep this contest's pool may be without the cluster
// going past budget, and fills in sizing's measurements. Existing spares are
// already in the cluster's usage, so the depth is have plus the new copies
// that fit, rounded down.
func (s *Service) copiesThatFit(ctx context.Context, contest Contest, budget int64, sizing *Sizing) (int, error) {
	size, err := s.templateBytes(ctx, contest)
	if err != nil {
		return 0, fmt.Errorf("size the pool for contest %s: %w", contest.ID, err)
	}
	if size <= 0 {
		// A database that exists measures megabytes, so a size of zero or less
		// means the template is gone; dividing by it would grant an unbounded
		// pool.
		return 0, fmt.Errorf("size the pool for contest %s: the template %s measures %d bytes",
			contest.ID, contest.Template, size)
	}
	used, err := s.cluster.ClusterBytes(ctx)
	if err != nil {
		return 0, fmt.Errorf("size the pool for contest %s: %w", contest.ID, err)
	}
	have, err := s.repo.SpareCount(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, fmt.Errorf("size the pool for contest %s: %w", contest.ID, err)
	}

	sizing.TemplateBytes, sizing.ClusterBytes, sizing.Budget = size, used, budget

	room := budget - used
	if room < 0 {
		room = 0
	}
	// Over budget grants nothing new but keeps the existing spares.
	return have + int(room/size), nil
}
