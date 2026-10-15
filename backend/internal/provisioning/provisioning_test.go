package provisioning_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
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
