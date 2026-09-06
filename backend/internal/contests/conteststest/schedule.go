package conteststest

import (
	"context"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// Schedule is an in-memory contests.ScheduleRepository, for the scheduler's
// own tests (contests/schedule_test.go). Nothing else in this package
// depends on it — see Scheduler's own doc for why it is not folded into the
// wider Contests fake.
type Schedule struct {
	// Acquired is TryLock's own answer, true unless a test stages otherwise
	// to stand for a losing replica in this tick's race for the lock.
	Acquired bool
	LockErr  error

	Started    []uuid.UUID
	StartedErr error

	Finished    []uuid.UUID
	FinishedErr error

	// LockCalls, RunningCalls and FinishedCalls count how many times each was
	// asked, so a test can prove a lost lock stops the tick before either
	// bulk move ever runs.
	LockCalls, RunningCalls, FinishedCalls int
}

var _ contests.ScheduleRepository = (*Schedule)(nil)

// NewSchedule returns a fake that wins the lock and moves nothing, until a
// test stages otherwise.
func NewSchedule() *Schedule { return &Schedule{Acquired: true} }

func (s *Schedule) TryLock(context.Context) (bool, error) {
	s.LockCalls++
	return s.Acquired, s.LockErr
}

func (s *Schedule) AdvanceRunning(context.Context) ([]uuid.UUID, error) {
	s.RunningCalls++
	return s.Started, s.StartedErr
}

func (s *Schedule) AdvanceFinished(context.Context) ([]uuid.UUID, error) {
	s.FinishedCalls++
	return s.Finished, s.FinishedErr
}
