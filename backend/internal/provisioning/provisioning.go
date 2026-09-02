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

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

var (
	// ErrNoSpare means the pool was empty, not that anything went wrong.
	ErrNoSpare = errors.New("no spare copy")
	// ErrNoInstance means this registration has no database yet.
	ErrNoInstance = errors.New("no instance")
)

// Instance is the database a registration works in.
type Instance struct {
	Database        string
	TemplateVersion int
	Status          string
}

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
}

// New assembles the service.
func New(repo Repository, cluster Cluster) *Service {
	return &Service{repo: repo, cluster: cluster}
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
	case err == nil && existing.TemplateVersion >= contest.Version:
		return existing.Database, nil
	case err == nil:
		// Theirs, but from a template that has since been rebuilt. Replacing
		// it here rather than waiting for the sweep means a participant who
		// arrives first is not the one who plays on old data.
		if err := s.replace(ctx, contest, existing.Database); err != nil {
			return "", err
		}
		if err := s.repo.Assign(ctx, contest.ID, registration, existing.Database, contest.Version); err != nil {
			return "", err
		}
		return existing.Database, nil
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

	made := 0
	for range depth - have {
		if err := ctx.Err(); err != nil {
			return made, nil
		}

		database := spareName(contest.ID)
		if err := s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy); err != nil {
			return made, err
		}
		if err := s.repo.AddSpare(ctx, contest.ID, database, contest.Version); err != nil {
			_ = s.cluster.Drop(context.WithoutCancel(ctx), database)
			return made, err
		}
		made++
	}
	return made, nil
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

// Ready reports whether the contest may start: every database it has must have
// come from the current template (section 4.2).
func (s *Service) Ready(ctx context.Context, contest Contest) (bool, error) {
	return s.repo.AllCurrent(ctx, contest.ID, contest.Version)
}

// replace makes a database new again, keeping its name.
func (s *Service) replace(ctx context.Context, contest Contest, database string) error {
	return s.cluster.CreateInstance(ctx, contest.Template, database, contest.Policy)
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
