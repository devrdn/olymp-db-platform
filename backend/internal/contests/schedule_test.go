package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// newScheduler assembles a Scheduler over the in-memory fakes, mirroring
// conteststest.NewFixture's own wiring for the pieces Scheduler actually
// uses.
func newScheduler() (*contests.Scheduler, *conteststest.Schedule, *conteststest.Sink, *conteststest.UnitOfWork) {
	repo := conteststest.NewSchedule()
	sink := conteststest.NewSink()
	uow := &conteststest.UnitOfWork{}
	return contests.NewScheduler(repo, audit.New(sink), uow), repo, sink, uow
}

func TestAdvanceDoesNothingWhenAnotherReplicaHoldsTheLock(t *testing.T) {
	scheduler, repo, sink, uow := newScheduler()
	repo.Acquired = false
	repo.Started = []uuid.UUID{uuid.New()}

	started, finished, err := scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if started != 0 || finished != 0 {
		t.Errorf("started, finished = %d, %d, want 0, 0 when the lock was lost", started, finished)
	}
	if repo.RunningCalls != 0 || repo.FinishedCalls != 0 {
		t.Error("a lost lock must stop the tick before either bulk move runs")
	}
	if len(sink.Entries) != 0 {
		t.Errorf("audit entries = %v, want none for a tick that moved nothing", sink.Actions())
	}
	if uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1", uow.Calls)
	}
}

func TestAdvanceMovesAndAuditsEveryContestTheLockLets(t *testing.T) {
	scheduler, repo, sink, uow := newScheduler()
	started := uuid.New()
	finished := uuid.New()
	repo.Started = []uuid.UUID{started}
	repo.Finished = []uuid.UUID{finished}

	gotStarted, gotFinished, err := scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if gotStarted != 1 || gotFinished != 1 {
		t.Errorf("started, finished = %d, %d, want 1, 1", gotStarted, gotFinished)
	}
	if uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1 — one tick, one transaction", uow.Calls)
	}

	if len(sink.Entries) != 2 {
		t.Fatalf("audit entries = %d, want 2 (one per moved contest)", len(sink.Entries))
	}
	for _, e := range sink.Entries {
		if e.Action != audit.ActionContestStatusChange {
			t.Errorf("action = %q, want %q", e.Action, audit.ActionContestStatusChange)
		}
		if e.ActorID != nil {
			t.Errorf("actor = %v, want nil — nobody asked for an automatic transition", e.ActorID)
		}
		if e.Entity != "contest" {
			t.Errorf("entity = %q, want contest", e.Entity)
		}
	}

	byEntity := map[string]audit.Entry{}
	for _, e := range sink.Entries {
		byEntity[e.EntityID] = e
	}
	startedChange, ok := byEntity[started.String()].Payload["changes"].(map[string]any)["status"]
	if !ok {
		t.Fatalf("no status change recorded for the started contest: %v", sink.Entries)
	}
	if from, ok := startedChange.(map[string]any)["from"]; !ok || from != contests.StatusPublished {
		t.Errorf("started contest's status change from = %v, want %q", from, contests.StatusPublished)
	}
	if to, ok := startedChange.(map[string]any)["to"]; !ok || to != contests.StatusRunning {
		t.Errorf("started contest's status change to = %v, want %q", to, contests.StatusRunning)
	}

	finishedChange := byEntity[finished.String()].Payload["changes"].(map[string]any)["status"].(map[string]any)
	if finishedChange["from"] != contests.StatusRunning || finishedChange["to"] != contests.StatusFinished {
		t.Errorf("finished contest's status change = %v, want running -> finished", finishedChange)
	}
}

func TestAdvanceRecordsNothingWhenTheLockIsWonAndNothingMoved(t *testing.T) {
	scheduler, _, sink, uow := newScheduler()

	started, finished, err := scheduler.Advance(context.Background())
	if err != nil {
		t.Fatalf("Advance() = %v", err)
	}
	if started != 0 || finished != 0 {
		t.Errorf("started, finished = %d, %d, want 0, 0", started, finished)
	}
	if len(sink.Entries) != 0 {
		t.Errorf("audit entries = %v, want none — an empty tick is not an event", sink.Actions())
	}
	if uow.Calls != 1 {
		t.Errorf("UnitOfWork.Calls = %d, want exactly 1", uow.Calls)
	}
}

func TestAdvanceFailsWithoutRecordingWhenTheAuditSinkFails(t *testing.T) {
	scheduler, repo, sink, _ := newScheduler()
	repo.Started = []uuid.UUID{uuid.New()}
	failure := errors.New("audit sink is down")
	sink.AppendManyErr = failure

	_, _, err := scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
}

func TestAdvanceFailsWhenTheBulkMoveFails(t *testing.T) {
	scheduler, repo, sink, _ := newScheduler()
	failure := errors.New("database is away")
	repo.StartedErr = failure

	_, _, err := scheduler.Advance(context.Background())
	if !errors.Is(err, failure) {
		t.Errorf("Advance() = %v, want an error wrapping %v", err, failure)
	}
	if repo.FinishedCalls != 0 {
		t.Error("a failed move to running must not still attempt the move to finished")
	}
	if len(sink.Entries) != 0 {
		t.Error("nothing should be audited when the move itself failed")
	}
}
