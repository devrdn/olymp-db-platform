package queryproxy_test

import (
	"context"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/participation"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/queryproxy"
	"github.com/google/uuid"
)

// quiet is the audit sink and unit of work of a test that does not read the
// trail.
type quiet struct{}

var quietTrail = audit.New(quiet{})

func (quiet) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (quiet) Append(context.Context, audit.Entry) error                    { return nil }
func (quiet) AppendMany(context.Context, []audit.Entry) error              { return nil }

// trail is the audit sink and the unit of work in one, so a test can tell an
// entry written inside the start's transaction from one written after it.
type trail struct {
	entries []audit.Entry
	loose   []audit.Entry
}

type inTxKey struct{}

func (tr *trail) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, inTxKey{}, true))
}

func (tr *trail) Append(ctx context.Context, e audit.Entry) error {
	tr.entries = append(tr.entries, e)
	if ctx.Value(inTxKey{}) == nil {
		tr.loose = append(tr.loose, e)
	}
	return nil
}

func (tr *trail) AppendMany(ctx context.Context, entries []audit.Entry) error {
	for _, e := range entries {
		if err := tr.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// peopleStore, contestReader and gameReader are the stores a test reads the
// console's row from in separate calls.
type peopleStore interface {
	ByUser(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error)
	contests.Starter
}

type contestReader interface {
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

type gameReader interface {
	Game(ctx context.Context, contestID uuid.UUID) (provisioning.Contest, error)
}

type instanceReader interface {
	Instance(ctx context.Context, registration uuid.UUID) (provisioning.Instance, error)
}

// consoleLookup is what a deployment hands both the admitter and the façade
// (postgres.Registrations).
type consoleLookup interface {
	participation.Lookup
	queryproxy.Lookup
}

// separateLookup reads the console's row in separate calls, in admission
// order, where postgres.Registrations reads it in one.
type separateLookup struct {
	people    peopleStore
	contests  contestReader
	games     gameReader
	instances instanceReader
}

func (d separateLookup) ForAccess(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, contests.Contest, error) {
	participant, err := d.people.ByUser(ctx, contestID, userID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	contest, err := d.contests.ByID(ctx, contestID)
	if err != nil {
		return contests.Participant{}, contests.Contest{}, err
	}
	contest.Translations = nil
	return participant, contest, nil
}

func (d separateLookup) ForRun(ctx context.Context, contestID, userID uuid.UUID) (queryproxy.LookupResult, error) {
	participant, contest, err := d.ForAccess(ctx, contestID, userID)
	if err != nil {
		return queryproxy.LookupResult{}, err
	}
	game, gameErr := d.games.Game(ctx, contestID)
	instance, instanceErr := d.instances.Instance(ctx, participant.ID)
	return queryproxy.LookupResult{
		Participant: participant, Contest: contest,
		Game: game, GameErr: gameErr,
		Instance: instance, InstanceErr: instanceErr,
	}, nil
}

// assembly is how newService builds the admitter and the façade.
type assembly struct {
	lookup  consoleLookup
	now     func() time.Time
	watcher participation.Watcher
}

type option func(*assembly)

// at asks the gate, and records starts, at now.
func at(now func() time.Time) option { return func(a *assembly) { a.now = now } }

// watchedBy reports every admitted request to watcher.
func watchedBy(watcher participation.Watcher) option {
	return func(a *assembly) { a.watcher = watcher }
}

// lookingUp reads the console's row through lookup instead of in separate
// calls.
func lookingUp(lookup consoleLookup) option { return func(a *assembly) { a.lookup = lookup } }

// newService assembles the façade the way internal/app does, over the given
// stores: one admitter, whose gate and audit trail they are, and the façade
// over it. Starts go to people.
func newService(people peopleStore, contestStore contestReader, games gameReader, databases queryproxy.Databases,
	runner queryproxy.Executor, gate *contests.Gate, recorder *audit.Recorder, uow storage.UnitOfWork, opts ...option) *queryproxy.Service {
	instances, _ := databases.(instanceReader)
	parts := assembly{lookup: separateLookup{people: people, contests: contestStore, games: games, instances: instances}}
	for _, opt := range opts {
		opt(&parts)
	}
	admitter := participation.New(participation.Config{
		Lookup: parts.lookup, People: people, Gate: gate,
		Audit: recorder, UnitOfWork: uow, Watcher: parts.watcher, Now: parts.now,
	})
	return queryproxy.New(admitter, parts.lookup, databases, runner)
}
