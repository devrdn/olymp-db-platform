package provisioning_test

import (
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

func TestTriggerWakesTheChannel(t *testing.T) {
	tender := provisioning.NewTender()
	tender.Trigger(uuid.New())

	select {
	case <-tender.C():
	case <-time.After(time.Second):
		t.Fatal("Trigger() did not wake the channel")
	}
}

func TestManyTriggersCoalesceIntoOnePendingWake(t *testing.T) {
	tender := provisioning.NewTender()

	for range 5 {
		tender.Trigger(uuid.New())
	}

	select {
	case <-tender.C():
	case <-time.After(time.Second):
		t.Fatal("five triggers produced no wake at all")
	}

	select {
	case <-tender.C():
		t.Fatal("a second wake was pending; five triggers before the first read must coalesce into one")
	default:
	}
}

// Under -race the concurrent burst also checks Trigger is goroutine-safe.
func TestTriggerNeverBlocksEvenWhenNobodyIsListening(t *testing.T) {
	tender := provisioning.NewTender()

	const callers = 200
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			tender.Trigger(uuid.New())
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("200 concurrent triggers with nobody reading C did not return — Trigger blocked")
	}

	// One wake is left pending.
	select {
	case <-tender.C():
	default:
		t.Fatal("no wake was left pending after 200 concurrent triggers")
	}
}
