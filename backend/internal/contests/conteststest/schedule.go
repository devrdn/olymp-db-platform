package conteststest

import (
	"context"
	"errors"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// Schedule is an in-memory contests.ScheduleRepository, for the scheduler's
// own tests (contests/schedule_test.go), held to the same answers as
// postgres.Contests by ScheduleRepositoryContract, which both run.
//
// It reads and moves the contests in its Contests store, the way
// postgres.Contests answers both interfaces from one table: a test stages a
// due or overdue contest by storing it in that status and window, and finds
// out what a tick did by reading the store back, so a contest DueToStart
// could never return cannot be handed to the scheduler as due.
type Schedule struct {
	// Contests is the store the schedule reads and moves. Its Clock is the
	// schedule's clock as well, the one the database's now() stands for:
	// whether a start or an end has arrived is decided by it, and a move
	// stamps UpdatedAt with it. NewSchedule sets it to FixtureNow.
	Contests *Contests

	// Acquired is TryLock's own answer, true unless a test stages otherwise
	// to stand for a losing replica in this tick's race for the lock.
	Acquired bool
	LockErr  error

	// DueErr, SetStatusErr and FinishedErr, when set, are what DueToStart,
	// SetStatus and AdvanceFinished return instead of reading or moving
	// anything: an infrastructure failure rather than a race any particular
	// contest lost. A race is staged on the store itself, with
	// Contests.SetStatusRaces.
	DueErr       error
	SetStatusErr error
	FinishedErr  error

	// Moved collects the ids SetStatus actually moved, in call order, so a
	// test can check which contests a tick started without re-deriving
	// CheckPublishable itself.
	Moved []uuid.UUID
	// GraceSeen records the grace Advance actually passed to AdvanceFinished
	// on the last call, so a test can prove Scheduler threads its own
	// configured grace through rather than comparing ends_at bare.
	GraceSeen time.Duration

	// LockCalls, DueCalls, SetStatusCalls and FinishedCalls count how many
	// times each was asked, so a test can prove a lost lock stops the tick
	// before any of the others ever run.
	LockCalls, DueCalls, SetStatusCalls, FinishedCalls int
}

var _ contests.ScheduleRepository = (*Schedule)(nil)

// NewSchedule returns a fake that wins the lock, over an empty contest store
// whose clock reads FixtureNow.
func NewSchedule() *Schedule {
	store := NewContests()
	store.Clock = func() time.Time { return FixtureNow }
	return &Schedule{Contests: store, Acquired: true}
}

// now is the store's clock. Without one there is no telling whether a window
// has opened, so it is an error rather than a guess.
func (s *Schedule) now() (time.Time, error) {
	if s.Contests.Clock == nil {
		return time.Time{}, errors.New("the schedule's contest store has no clock")
	}
	return s.Contests.Clock(), nil
}

func (s *Schedule) TryLock(context.Context) (bool, error) {
	s.LockCalls++
	return s.Acquired, s.LockErr
}

// DueToStart returns every published contest whose start is at or before the
// clock, as ByID would read it, and changes nothing.
func (s *Schedule) DueToStart(context.Context) ([]contests.Contest, error) {
	s.DueCalls++
	if s.DueErr != nil {
		return nil, s.DueErr
	}
	now, err := s.now()
	if err != nil {
		return nil, err
	}
	var due []contests.Contest
	for _, id := range s.Contests.order {
		c := s.Contests.byID[id]
		if c.Status == contests.StatusPublished && c.StartsAt != nil && !c.StartsAt.After(now) {
			due = append(due, served(c))
		}
	}
	return due, nil
}

// SetStatus is the store's own, as postgres.Contests uses one method for
// both interfaces.
func (s *Schedule) SetStatus(ctx context.Context, id uuid.UUID, from, to string) error {
	s.SetStatusCalls++
	if s.SetStatusErr != nil {
		return s.SetStatusErr
	}
	if err := s.Contests.SetStatus(ctx, id, from, to); err != nil {
		return err
	}
	s.Moved = append(s.Moved, id)
	return nil
}

// AdvanceFinished finishes every running contest whose end plus grace is at
// or before the clock, stamping UpdatedAt with it, and returns their ids. A
// contest with no end never matches.
func (s *Schedule) AdvanceFinished(_ context.Context, grace time.Duration) ([]uuid.UUID, error) {
	s.FinishedCalls++
	s.GraceSeen = grace
	if s.FinishedErr != nil {
		return nil, s.FinishedErr
	}
	now, err := s.now()
	if err != nil {
		return nil, err
	}
	var finished []uuid.UUID
	for _, id := range s.Contests.order {
		c := s.Contests.byID[id]
		if c.Status != contests.StatusRunning || c.EndsAt == nil || c.EndsAt.Add(grace).After(now) {
			continue
		}
		c.Status = contests.StatusFinished
		c.UpdatedAt = now
		s.Contests.byID[id] = c
		finished = append(finished, id)
	}
	return finished, nil
}
