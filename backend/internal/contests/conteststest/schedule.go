package conteststest

import (
	"context"
	"errors"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// Schedule is an in-memory contests.ScheduleRepository that reads and moves
// the contests in its Contests store, as postgres.Contests answers both
// interfaces from one table. A test stages a contest in the store and reads
// back what a tick did, so the scheduler is never handed a contest
// DueToStart could not return.
type Schedule struct {
	// Contests is the store the schedule reads and moves. Its Clock stands
	// for the database's now(): it decides whether a window has arrived and
	// stamps UpdatedAt.
	Contests *Contests

	// Acquired is TryLock's answer; false stands for a replica that lost the
	// lock.
	Acquired bool
	LockErr  error

	// DueErr, SetStatusErr and FinishedErr stand for infrastructure
	// failures. A race is staged with Contests.SetStatusRaces instead.
	DueErr       error
	SetStatusErr error
	FinishedErr  error

	// Moved lists the ids SetStatus moved, in call order.
	Moved []uuid.UUID
	// GraceSeen is the grace the last AdvanceFinished received, so a test can
	// prove the Scheduler passes its configured grace through.
	GraceSeen time.Duration

	// The call counters let a test prove a lost lock stops the tick before
	// anything else runs.
	LockCalls, DueCalls, SetStatusCalls, FinishedCalls int
}

var _ contests.ScheduleRepository = (*Schedule)(nil)

// NewSchedule returns a fake that wins the lock, over an empty store whose
// clock reads FixtureNow.
func NewSchedule() *Schedule {
	store := NewContests()
	store.Clock = func() time.Time { return FixtureNow }
	return &Schedule{Contests: store, Acquired: true}
}

// now is an error without a clock: there is no telling whether a window has
// opened.
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

// DueToStart returns published contests whose start is at or before the
// clock, and changes nothing.
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

// SetStatus is the store's, as postgres.Contests uses one method for both
// interfaces.
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

// AdvanceFinished finishes running contests whose end plus grace is at or
// before the clock. A contest with no end never matches.
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
