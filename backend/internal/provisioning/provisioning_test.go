package provisioning_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

func TestParticipantsWaitingForACopyShareTheWorkerBudget(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 2)
	fake := &cluster{slow: 40 * time.Millisecond}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).WithWorkers(1)

	var wg sync.WaitGroup
	errs := make([]error, len(people))
	for i, registration := range people {
		wg.Go(func() {
			_, errs[i] = service.Ensure(t.Context(), contest, registration)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if made, _ := fake.counts(); made != 2 {
		t.Fatalf("made %d databases, want 2", made)
	}
	if peak := fake.highWater(); peak != 1 {
		t.Fatalf("%d copies were being made at once with one worker", peak)
	}
}

func TestAParticipantWhoStopsWaitingGetsNoCopy(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 2)
	fake := &cluster{slow: 300 * time.Millisecond}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).WithWorkers(1)

	first := make(chan error, 1)
	go func() {
		_, err := service.Ensure(t.Context(), contest, people[0])
		first <- err
	}()
	waitUntil(t, func() bool { return fake.highWater() == 1 })

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := service.Ensure(ctx, contest, people[1]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the context's", err)
	}

	if err := <-first; err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if made, _ := fake.counts(); made != 1 {
		t.Fatalf("made %d databases, want only the first participant's", made)
	}
}

func TestTwoRequestsOfOneParticipantMakeOneCopy(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{slow: 40 * time.Millisecond}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)

	var wg sync.WaitGroup
	names := make([]string, 2)
	errs := make([]error, 2)
	for i := range names {
		wg.Go(func() {
			names[i], errs[i] = service.Ensure(t.Context(), contest, people[0])
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if names[0] != names[1] {
		t.Fatalf("two databases for one participant: %q and %q", names[0], names[1])
	}
	if made, _ := fake.counts(); made != 1 {
		t.Fatalf("made %d databases for one participant, want 1", made)
	}
}

// The second copy's DROP … WITH (FORCE) would otherwise remove the first one
// halfway through, so two presses of the reset at once must take turns.
func TestTwoResetsOfOneParticipantCopyOneAfterTheOther(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{slow: 40 * time.Millisecond}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)
	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			errs[i] = service.Reset(t.Context(), uuid.New(), contest, people[0])
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if made, _ := fake.counts(); made != 3 {
		t.Fatalf("made %d databases, want the first copy and one per reset", made)
	}
	if peak := fake.highWater(); peak != 1 {
		t.Fatalf("%d copies of one participant's database were being made at once", peak)
	}
}

// The participant pressed it, so the trail names them, not the system.
func TestAResetIsRecordedWithTheParticipantAsItsActor(t *testing.T) {
	service, _, s, contest, people := serviceWithAudit(t, t.Context(), 1)
	participant := uuid.New()
	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if err := service.Reset(t.Context(), participant, contest, people[0]); err != nil {
		t.Fatalf("reset: %v", err)
	}

	entries := entriesFor(s, contest.ID)
	if len(entries) != 1 {
		t.Fatalf("%d entries recorded against the contest, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != audit.ActionGameInstanceReset {
		t.Fatalf("action = %q, want %q", entry.Action, audit.ActionGameInstanceReset)
	}
	if entry.ActorID == nil || *entry.ActorID != participant {
		t.Fatalf("actor = %v, want the participant %v", entry.ActorID, participant)
	}
	if entry.Payload["database"] != database {
		t.Fatalf("payload names database %v, want %q", entry.Payload["database"], database)
	}
	if entry.Payload["registration_id"] != people[0].String() {
		t.Fatalf("payload names registration %v, want %s", entry.Payload["registration_id"], people[0])
	}
	if entry.Payload["template_version"] != contest.Version {
		t.Fatalf("payload names template version %v, want %d", entry.Payload["template_version"], contest.Version)
	}
}

func TestAResetOfNothingRecordsNothing(t *testing.T) {
	service, _, s, contest, people := serviceWithAudit(t, t.Context(), 1)

	if err := service.Reset(t.Context(), uuid.New(), contest, people[0]); !errors.Is(err, provisioning.ErrNoInstance) {
		t.Fatalf("error = %v, want ErrNoInstance", err)
	}
	if entries := entriesFor(s, contest.ID); len(entries) != 0 {
		t.Fatalf("%d entries recorded for a reset that copied nothing", len(entries))
	}
}

// The old database is gone once the copy starts, so a participant leaving
// mid-copy must not leave a 'ready' row naming nothing.
func TestAResetWhoseCallerLeavesMidCopyStillFinishes(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)
	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	fake.mu.Lock()
	fake.slow = 100 * time.Millisecond
	fake.mu.Unlock()

	ctx, leave := context.WithCancel(t.Context())
	reset := make(chan error, 1)
	go func() { reset <- service.Reset(ctx, uuid.New(), contest, people[0]) }()
	waitUntil(t, func() bool { return fake.inFlight() == 1 })
	leave()

	if err := <-reset; err != nil {
		t.Fatalf("reset: %v; the caller leaving after the turn was taken must not stop it", err)
	}
	if !fake.exists(database) {
		t.Fatalf("%s is gone after the reset", database)
	}
	if status := statusOf(t, t.Context(), database); status != "ready" {
		t.Fatalf("the row is %q, want ready over the fresh copy", status)
	}
}

// A copy that failed after its name was dropped leaves the row saying so, so
// the participant's next action rebuilds it rather than being handed nothing.
func TestAResetWhoseCopyFailsLeavesTheRowDropped(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)
	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	fake.mu.Lock()
	fake.copyFail = errors.New("copy the template: no space left on device")
	fake.mu.Unlock()

	if err := service.Reset(t.Context(), uuid.New(), contest, people[0]); err == nil {
		t.Fatal("a failed copy was reported as a reset")
	}
	if fake.exists(database) {
		t.Fatalf("%s still exists; the fake should have dropped it first", database)
	}
	if status := statusOf(t, t.Context(), database); status != "dropped" {
		t.Fatalf("the row is %q over a database that is gone, want dropped", status)
	}
}

// The copy's bound running out is itself a failed copy, and the cleanup must
// not inherit the deadline that just passed.
func TestAResetWhoseCopyOutlastsItsBoundLeavesTheRowDropped(t *testing.T) {
	service, fake, s, contest, people := serviceWithAudit(t, t.Context(), 1)
	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	service.WithCopyTimeout(50 * time.Millisecond)
	fake.mu.Lock()
	fake.slow = time.Second
	fake.mu.Unlock()
	participant := uuid.New()

	if err := service.Reset(t.Context(), participant, contest, people[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the copy's deadline", err)
	}
	if fake.exists(database) {
		t.Fatalf("%s exists after a copy that never finished", database)
	}
	if status := statusOf(t, t.Context(), database); status != "dropped" {
		t.Fatalf("the row is %q over a database that is gone, want dropped", status)
	}

	// Every other place that marks a row dropped says why on the trail.
	entries := entriesFor(s, contest.ID)
	if len(entries) != 1 {
		t.Fatalf("%d entries recorded against the contest, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Action != audit.ActionGameInstanceReset || entry.Payload["failed"] != true {
		t.Fatalf("entry = %s %v, want a failed reset", entry.Action, entry.Payload)
	}
	if entry.ActorID == nil || *entry.ActorID != participant {
		t.Fatalf("actor = %v, want the participant %v", entry.ActorID, participant)
	}
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("the condition never held")
		}
		time.Sleep(time.Millisecond)
	}
}
