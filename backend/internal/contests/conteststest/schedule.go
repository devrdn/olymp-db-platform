package conteststest

import (
	"context"
	"time"

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

	// Due stages what DueToStart returns: full contest rows, since the
	// scheduler now re-runs the publish gate against each of them before
	// deciding to move anything (finding 1). The story and questions behind
	// that gate live in whatever Stories and Questions fakes a test wires up
	// alongside this one, keyed by the same contest ids.
	Due    []contests.Contest
	DueErr error

	// Raced maps a contest id to the error its SetStatus call should return
	// instead of moving it — ErrStatusChanged or ErrNotFound, standing for a
	// concurrent manual Transition (or a delete) that decided the contest's
	// fate between DueToStart's read and Advance's write. Absent from the
	// map, or a nil value, means the call succeeds.
	Raced map[uuid.UUID]error
	// SetStatusErr, when set, is what every SetStatus call returns instead
	// of moving anything or consulting Raced — an infrastructure failure
	// rather than a race any particular contest lost.
	SetStatusErr error
	// Moved collects the ids SetStatus actually moved to running, in call
	// order, so a test can check which of Due's contests were started
	// without re-deriving CheckPublishable itself.
	Moved []uuid.UUID

	Finished    []uuid.UUID
	FinishedErr error
	// GraceSeen records the grace Advance actually passed to AdvanceFinished
	// on the last call, so a test can prove Scheduler threads its own
	// configured grace through rather than comparing ends_at bare (finding
	// C-07).
	GraceSeen time.Duration

	// LockCalls, DueCalls, SetStatusCalls and FinishedCalls count how many
	// times each was asked, so a test can prove a lost lock stops the tick
	// before any of the others ever run.
	LockCalls, DueCalls, SetStatusCalls, FinishedCalls int
}

var _ contests.ScheduleRepository = (*Schedule)(nil)

// NewSchedule returns a fake that wins the lock and moves nothing, until a
// test stages otherwise.
func NewSchedule() *Schedule { return &Schedule{Acquired: true} }

func (s *Schedule) TryLock(context.Context) (bool, error) {
	s.LockCalls++
	return s.Acquired, s.LockErr
}

func (s *Schedule) DueToStart(context.Context) ([]contests.Contest, error) {
	s.DueCalls++
	return s.Due, s.DueErr
}

func (s *Schedule) SetStatus(_ context.Context, id uuid.UUID, from, to string) error {
	s.SetStatusCalls++
	if s.SetStatusErr != nil {
		return s.SetStatusErr
	}
	if err := s.Raced[id]; err != nil {
		return err
	}
	s.Moved = append(s.Moved, id)
	return nil
}

func (s *Schedule) AdvanceFinished(_ context.Context, grace time.Duration) ([]uuid.UUID, error) {
	s.FinishedCalls++
	s.GraceSeen = grace
	return s.Finished, s.FinishedErr
}
