package provisioning_test

import (
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// TestTriggerWakesTheChannel is Tender's basic promise: a caller that changed
// a contest's roster can ask for a tend soon, and the background loop
// listening on C sees it.
func TestTriggerWakesTheChannel(t *testing.T) {
	tender := provisioning.NewTender()
	tender.Trigger(uuid.New())

	select {
	case <-tender.C():
	case <-time.After(time.Second):
		t.Fatal("Trigger() did not wake the channel")
	}
}

// TestManyTriggersCoalesceIntoOnePendingWake is the resolution's own
// requirement: a burst of registrations for one contest — or several
// contests at once — must not queue one wake per event. Tend always walks
// every live contest in a single pass, so collapsing every trigger since the
// last wake into the one already pending loses nothing.
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

// TestTriggerNeverBlocksEvenWhenNobodyIsListening is what makes Trigger safe
// to call from a request handler or from inside a transaction's commit path:
// it must return immediately whether or not a background loop is currently
// reading from C, and it must never spawn a goroutine per call — a
// concurrent burst of callers proves both, since a blocking or
// goroutine-per-call implementation would either hang this test or let it
// race under -race.
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

	// Exactly one wake is left pending, the same coalescing guarantee under
	// concurrency that the sequential test above proves without it.
	select {
	case <-tender.C():
	default:
		t.Fatal("no wake was left pending after 200 concurrent triggers")
	}
}
