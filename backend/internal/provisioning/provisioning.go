// Package provisioning gives every participant their own copy of a contest's
// database, without the cluster feeling it.
//
// It answers one question: which database does this participant work in, and
// how did it get there. The awkward part is timing rather than logic —
// `CREATE DATABASE … TEMPLATE` is not free, and a hundred of them at the
// moment a contest starts is the one operation that can spoil an olympiad
// before a single query runs. So copies are made early and kept spare, and the
// moment a participant needs one is a single row update.
//
// What it deliberately does not do: execute anything a participant wrote, or
// decide what they may run. That is internal/queryrunner and
// internal/sqlpolicy. It also does not know how to build a database — the DDL
// belongs to internal/gamedb, which this drives through a narrow interface so
// that "a database per participant" can be swapped for something else if the
// pilot says so (section 4.2, plan B).
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
	// building. Nobody's mistake, and not a fact about anybody's query.
	ErrNoGame = errors.New("no game database")
)

// Instance is the database a registration works in.
type Instance struct {
	Database        string
	TemplateVersion int
	Status          string
}

// InstanceStatusDropped is the terminal status the schema has carried since
// migration 3 (docs/ARCHITECTURE.md §2.4): Reclaim leaves the row behind in
// this status once the database itself is gone from the cluster. Ensure is
// the one other place besides gameinstances.go that has to know the literal
// — a dropped row is history, not a database it may hand back, whatever
// template version it still carries from the moment it was last live.
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
	AllCurrent(ctx context.Context, contest uuid.UUID, version int) (bool, error)
	// Live lists the contests whose pool is worth keeping stocked: published
	// or running, with a template that finished building.
	Live(ctx context.Context) ([]Contest, error)
	// Game returns one contest's game, or ErrNoGame. Named apart from Of
	// above, which answers about a registration rather than a contest.
	Game(ctx context.Context, contestID uuid.UUID) (Contest, error)
	// Reclaimable lists up to limit not-yet-dropped instances of a contest
	// that reached the finished or archived status longer ago than its own
	// grace period — or, for a contest that never configured one, longer ago
	// than installationGraceMin. limit is what keeps one tick from issuing an
	// unbounded run of DROP DATABASE against the cluster (Service.Reclaim's
	// own doc explains the figure it passes). See ReclaimCandidate and
	// Service.Reclaim.
	Reclaimable(ctx context.Context, installationGraceMin, limit int) ([]ReclaimCandidate, error)
	// MarkDropped moves one instance to the terminal 'dropped' status. The row
	// stays — deleting it would leave an organizer's audit search with
	// nothing to point to once the database itself is gone.
	MarkDropped(ctx context.Context, database string) error
	// ReclaimableTemplates lists up to limit contest templates that may be
	// dropped: the contest is finished or archived past its grace, its
	// template is a built database ('ready'), and every one of its instances
	// is already gone ('dropped' or never provisioned at all). That last
	// condition is the ordering Service.Reclaim's own doc talks about — a
	// template is what instances are copied from, so it is never offered
	// ahead of them.
	ReclaimableTemplates(ctx context.Context, installationGraceMin, limit int) ([]TemplateCandidate, error)
	// MarkTemplateDropped moves one contest's template to the terminal
	// 'dropped' status, the same convention MarkDropped keeps for an
	// instance's row.
	MarkTemplateDropped(ctx context.Context, contestID uuid.UUID) error
	// Instances lists up to limit of one contest's rows — spare copies,
	// participants' own, and the dropped ones kept as history — newest
	// last. limit is the caller's bound rather than the query's own, so
	// Service.Instances can ask for one more than it will show and know
	// whether there are more (CLAUDE.md rule 2).
	Instances(ctx context.Context, contest uuid.UUID, limit int) ([]InstanceRecord, error)
	// InstanceNamed reads one row of a contest by database name, or
	// ErrInstanceNotFound. Scoped to the contest deliberately: db_name is
	// unique across the installation, so a lookup by name alone would let a
	// contest-scoped permission reach another contest's database.
	InstanceNamed(ctx context.Context, contest uuid.UUID, database string) (InstanceRecord, error)
}

// Cluster is the part of the game cluster this service drives.
//
// Narrow on purpose. Nothing here says "a database per participant" beyond the
// names, so the alternative section 4.2 keeps in reserve — a schema per
// participant — is a different implementation of these three methods rather
// than a different service.
type Cluster interface {
	CreateInstance(ctx context.Context, template, instance string, policy sqlpolicy.Policy) error
	Drop(ctx context.Context, name string) error
	DatabaseSize(ctx context.Context, name string) (int64, error)
	// DatabaseSizes is DatabaseSize for a whole list, in one round trip.
	// Apart from it rather than a loop over it, because the organizer's
	// database list asks about every copy a contest owns at once and a
	// round trip each would be hundreds of them for one screen. Names that
	// no longer exist are absent from the result rather than an error: the
	// list is read from the core database and the cluster is a second system
	// that may already have moved on.
	DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error)
	// DropIdle removes name only if nobody is connected to it, and reports
	// whether it did. Unlike Drop, it never forces a connection closed — the
	// reclaim sweep (its only caller) has no way to tell a forgotten session
	// apart from a query the Query Runner is still running, so it must never
	// assume the former. See gamedb.Provisioner.DropIdle's own doc.
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
	// audit and uow are set by WithAudit. Both nil until then, which Reclaim
	// treats as "record nothing" — see its own doc for who actually leaves
	// them unset.
	audit *audit.Recorder
	uow   storage.UnitOfWork
}

// DefaultWorkers is how many copies are made at once. Section 4.2 says two to
// four: enough that a pool fills in reasonable time, few enough that filling
// it is never what the cluster is busy doing.
const DefaultWorkers = 3

// ReclaimBatchLimit bounds how many instances (and, separately, how many
// templates) one Reclaim pass drops.
//
// Without it, the first tick after this sweep is deployed pays for every
// contest that finished before it existed in one go: the grace is measured
// from a contest's own updated_at, already months old for anything already
// finished, so nothing about the grace slows that first tick down — it is
// entirely history by the time the sweep can see it. A cluster asked for
// thousands of sequential DROP DATABASE statements in the minutes after boot
// is the deploy-day outage this number exists to prevent.
//
// 200 is chosen to drain a real backlog in a handful of ticks — Reclaim runs
// every ten minutes (internal/app/background.go), so even a few thousand
// long-finished instances clear within a day — while keeping one tick's own
// work small enough that it is done well before the next tick starts, on a
// cluster this platform's own numbers (section 4.2: a pool of a few spares
// per contest) never come close to needing all of at once.
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

// WithAudit lets Reclaim record what it drops, in the same core-database
// transaction as marking the row dropped — every real deployment supplies
// this (internal/app wires it beside every other background job's own
// audit). Left unset, Reclaim still drops databases and marks rows; it simply
// audits nothing, which is only ever correct in a test exercising Reclaim
// apart from the audit trail.
func (s *Service) WithAudit(rec *audit.Recorder, uow storage.UnitOfWork) *Service {
	s.audit = rec
	s.uow = uow
	return s
}

// Ensure returns the database this registration works in, making one if there
// is none.
//
// The order is cheapest first. An instance that already exists and came from
// the current template is the common case and costs one read. A spare copy is
// one row update. Only an empty pool pays for CREATE DATABASE while somebody
// waits, which is the case the pool exists to make rare.
func (s *Service) Ensure(ctx context.Context, contest Contest, registration uuid.UUID) (string, error) {
	switch existing, err := s.repo.Of(ctx, registration); {
	case err == nil && existing.Status != InstanceStatusDropped && existing.TemplateVersion >= contest.Version:
		// Theirs, current, and actually there.
		return existing.Database, nil
	case err == nil && existing.Status != InstanceStatusDropped:
		// Theirs, but from a template that has since been rebuilt. Replacing
		// it here rather than waiting for the sweep means a participant who
		// arrives first is not the one who plays on old data.
		return s.rebuildExisting(ctx, contest, registration, existing.Database)
	case err == nil:
		// existing.Status == InstanceStatusDropped: the row survives every
		// reclaim as history (gameinstances.MarkDropped's own doc), but the
		// database it names is gone from the cluster. The version comparison
		// above cannot catch this — status, not how current the template was
		// when the row was last written, is the fact that says whether the
		// database still exists — so a dropped row must never be handed back
		// as though it still had a live database behind it, whatever version
		// it happens to carry. Today this only reaches a finished contest's
		// own reclaimed instance; it also covers the one edit away — a
		// transition back out of 'finished' — that would otherwise hand a
		// returning participant a raw "database does not exist" instead of a
		// rebuilt copy.
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

	// The pool was empty. Section 4.2 calls this the late registration, and it
	// is the only path where somebody waits for a copy.
	database := instanceName(contest.ID, registration)
	if err := s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy); err != nil {
		return "", err
	}
	if err := s.repo.Assign(ctx, contest.ID, registration, database, contest.Version); err != nil {
		// The database exists and nothing points at it. Removed, so the next
		// attempt starts from a clean cluster rather than colliding.
		_ = s.cluster.Drop(context.WithoutCancel(ctx), database)
		return "", err
	}
	return database, nil
}

// TopUp brings the pool up to depth, and reports how many copies it made.
//
// Made one at a time by a bounded number of workers, never in a burst: the
// whole point of the pool is that CREATE DATABASE happens when nothing depends
// on it.
func (s *Service) TopUp(ctx context.Context, contest Contest, depth int) (int, error) {
	have, err := s.repo.SpareCount(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, err
	}
	want := depth - have
	if want <= 0 {
		return 0, nil
	}

	// A bounded number of workers rather than a loop or a burst. `CREATE
	// DATABASE … TEMPLATE` is the one operation that can spoil an olympiad
	// before a query runs, so the pool is filled by two or three at a time and
	// never by however many are missing.
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

// addSpare makes one copy and records it.
func (s *Service) addSpare(ctx context.Context, contest Contest) error {
	database := spareName(contest.ID)
	if err := s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy); err != nil {
		return err
	}
	if err := s.repo.AddSpare(ctx, contest.ID, database, contest.Version); err != nil {
		// The database exists and nothing points at it. Removed, so the
		// cluster does not accumulate copies nobody can find.
		_ = s.cluster.Drop(context.WithoutCancel(ctx), database)
		return err
	}
	return nil
}

// Invalidate removes every database of this contest made from an older
// template, and reports how many.
//
// Both the claimed and the free ones. A participant must not play on old data
// or old grants, and a spare copy of an old version is a trap waiting for
// whoever registers next.
func (s *Service) Invalidate(ctx context.Context, contest Contest) (int, error) {
	stale, err := s.repo.Stale(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, err
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

// Reset gives a participant their starting database back, under whatever they
// have open.
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

// Tend keeps every live contest's pool where it should be: stale copies gone,
// depth topped up. It reports how many databases it made and removed.
//
// The background half of section 4.2. Nothing here is urgent — that is the
// point: CREATE DATABASE happens while nobody is waiting, so that the moment a
// participant needs a copy is a row update. A contest that fails is logged and
// the others are still tended; one broken template must not stop the rest.
func (s *Service) Tend(ctx context.Context, depth func(Contest) int) (made, dropped int, err error) {
	contests, err := s.repo.Live(ctx)
	if err != nil {
		return 0, 0, err
	}

	var failures []error
	for _, contest := range contests {
		// Invalidation first: a stale copy still counts against the pool's
		// depth, and topping up before removing them would make copies nobody
		// can use.
		removed, err := s.Invalidate(ctx, contest)
		dropped += removed
		if err != nil {
			failures = append(failures, fmt.Errorf("contest %s: %w", contest.ID, err))
			continue
		}

		added, err := s.TopUp(ctx, contest, depth(contest))
		made += added
		if err != nil {
			failures = append(failures, fmt.Errorf("contest %s: %w", contest.ID, err))
		}
	}
	return made, dropped, errors.Join(failures...)
}

// Quota is how large one participant's database may grow.
//
// A multiple of the template rather than an absolute number, because the
// template is the only thing that says how large a contest's data legitimately
// is: a game with a hundred rows and one with a million should not share a
// figure somebody typed into a configuration file once.
func (s *Service) Quota(ctx context.Context, contest Contest) (int64, error) {
	size, err := s.cluster.DatabaseSize(ctx, contest.Template)
	if err != nil {
		return 0, err
	}

	ratio := contest.Policy.DiskQuotaRatio
	if ratio <= 0 {
		ratio = sqlpolicy.DefaultDiskQuotaRatio
	}
	// A floor, because a template of a few kilobytes would otherwise give a
	// participant a quota they exhaust with one INSERT — and the point is to
	// bound a runaway, not to make ordinary work fail.
	const smallest = 16 << 20
	if quota := size * int64(ratio); quota > smallest {
		return quota, nil
	}
	return smallest, nil
}

// Ready reports whether the contest may start: every database it has must have
// come from the current template (section 4.2).
func (s *Service) Ready(ctx context.Context, contest Contest) (bool, error) {
	return s.repo.AllCurrent(ctx, contest.ID, contest.Version)
}

// replace makes a database new again, keeping its name.
func (s *Service) replace(ctx context.Context, contest Contest, database string) error {
	return s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy)
}

// rebuildExisting makes database new again under its existing name and
// reassigns it to registration, current and 'ready'. Ensure's own two
// "this row cannot be handed back as-is" branches — a stale template version
// and a dropped instance — repair themselves identically once each has
// decided a rebuild is warranted; only the reason differs.
func (s *Service) rebuildExisting(ctx context.Context, contest Contest, registration uuid.UUID, database string) (string, error) {
	if err := s.replace(ctx, contest, database); err != nil {
		return "", err
	}
	if err := s.repo.Assign(ctx, contest.ID, registration, database, contest.Version); err != nil {
		return "", err
	}
	return database, nil
}

// The names. Short halves of two identifiers, which is enough to be unique
// across one installation and short enough for PostgreSQL's 63 characters —
// and readable enough that somebody looking at a list of databases can tell
// which contest a stray one belongs to.
func instanceName(contest, registration uuid.UUID) string {
	return "game_c" + short(contest) + "_u" + short(registration)
}

func spareName(contest uuid.UUID) string {
	return "game_pool_c" + short(contest) + "_" + short(uuid.New())
}

func short(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "")[:12]
}

// Databases is the shape a caller needs to report on the pool.
func (s *Service) Databases(ctx context.Context, contest Contest) (spare int, err error) {
	spare, err = s.repo.SpareCount(ctx, contest.ID, contest.Version)
	if err != nil {
		return 0, fmt.Errorf("read the pool depth: %w", err)
	}
	return spare, nil
}
