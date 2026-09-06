package contests

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// ScheduleRepository is what the background scheduler needs from storage
// (docs/ARCHITECTURE.md §8): the advisory lock one tick competes for, and the
// two bulk moves a tick may make once it holds that lock.
//
// A narrow interface of its own rather than three more methods on
// Repository: nothing else in this package ever calls any of these three —
// only Scheduler.Advance does, and Repository already answers a different
// question ("what may be changed, by whom") for every one of its other
// callers.
type ScheduleRepository interface {
	// TryLock attempts, inside the ambient transaction, the advisory lock
	// every replica's tick competes for. It is released automatically when
	// that transaction ends — committed or rolled back — so it is never held
	// between ticks: a replica that dies mid-tick leaves nothing for the next
	// one to wait on, and costs it nothing but the tick it missed. false
	// means another replica already holds it this tick; the caller does no
	// further work and simply lets its own transaction end.
	TryLock(ctx context.Context) (bool, error)
	// AdvanceRunning moves every published contest whose starts_at has
	// arrived, by the core database's own clock, to running, and returns
	// their ids so the caller can audit exactly those.
	AdvanceRunning(ctx context.Context) ([]uuid.UUID, error)
	// AdvanceFinished moves every running contest whose ends_at has passed,
	// by the core database's own clock, to finished, and returns their ids.
	// A contest with no ends_at (individual timing needs none to publish,
	// see CheckPublishable) never matches, and stays running until an
	// organizer moves it by hand.
	AdvanceFinished(ctx context.Context) ([]uuid.UUID, error)
}

// Scheduler moves contests along their published → running → finished
// lifecycle without an organizer asking (§8).
//
// Kept apart from Service on purpose: everything it needs is these three
// storage methods, a unit of work and the audit trail, and nothing else
// Service carries (the publish gate, submissions, sequencing, the domain's
// own clock for enrollment deadlines) has anything to do with a background
// tick. Folding it into Service would mean every test and every wiring site
// that builds one — most of which never call Advance — would have to supply
// a ScheduleRepository they do not use.
//
// One tick is one call to Advance: it competes for the lock, and either
// loses it (another replica has this tick, and does nothing further) or does
// the whole move-and-audit atomically before releasing it. The closing
// guarantee this whole feature exists to keep honest does not run through
// here: Submit (submission.go) checks Deadline against the core database's
// own clock in the same transaction as the answer it writes, so a late
// answer is refused even if Advance never runs again. What Advance owns is
// what the interface shows — the status a participant's screen and events
// channel report — and running late, whether by one tick or by however long
// a dead replica leaves the lock uncontested, never lets anybody in past
// their own window or keeps anybody answering past their own deadline; it
// only leaves the status saying "running" a little longer than the wall
// clock would have (§8's own words: "статус и SSE-события влияют только на
// UI").
type Scheduler struct {
	repo  ScheduleRepository
	audit *audit.Recorder
	uow   storage.UnitOfWork
}

// NewScheduler assembles the background scheduler.
func NewScheduler(repo ScheduleRepository, audit *audit.Recorder, uow storage.UnitOfWork) *Scheduler {
	return &Scheduler{repo: repo, audit: audit, uow: uow}
}

// Advance runs one tick: try for the lock, and if it is won, move every
// contest whose window opened or closed, auditing exactly those, all inside
// the one transaction the lock lives in.
//
// started and finished count the contests moved this tick — both zero, with
// a nil error, is the ordinary outcome of most ticks, whether because
// nothing's window has turned or because another replica already had this
// one.
func (s *Scheduler) Advance(ctx context.Context) (started, finished int, err error) {
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.TryLock(ctx)
		if err != nil {
			return fmt.Errorf("acquire the schedule lock: %w", err)
		}
		if !ok {
			return nil
		}

		startedIDs, err := s.repo.AdvanceRunning(ctx)
		if err != nil {
			return fmt.Errorf("advance contests to running: %w", err)
		}
		finishedIDs, err := s.repo.AdvanceFinished(ctx)
		if err != nil {
			return fmt.Errorf("advance contests to finished: %w", err)
		}
		started, finished = len(startedIDs), len(finishedIDs)

		entries := make([]audit.Entry, 0, started+finished)
		for _, id := range startedIDs {
			entries = append(entries, scheduleEntry(id, StatusPublished, StatusRunning))
		}
		for _, id := range finishedIDs {
			entries = append(entries, scheduleEntry(id, StatusRunning, StatusFinished))
		}
		if len(entries) == 0 {
			return nil
		}
		return s.audit.RecordMany(ctx, entries)
	})
	return started, finished, err
}

// scheduleEntry is one contest's automatic transition, in the same shape a
// manual one takes (Service.Transition) except for the actor: nil, because
// nobody asked for this one — exactly the case audit.Entry.ActorID's own doc
// names ("nil for system events such as a scheduled contest transition").
func scheduleEntry(contestID uuid.UUID, from, to string) audit.Entry {
	changes := audit.NewChanges()
	changes.Set("status", from, to)
	return audit.Entry{
		Action:   audit.ActionContestStatusChange,
		Entity:   "contest",
		EntityID: contestID.String(),
		Payload:  changes.Payload(),
	}
}
