package mailing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
)

func TestAnEnqueuedMessageIsStoredAndWakesTheSender(t *testing.T) {
	queue := &fakeQueue{}
	outbox := mailing.NewOutbox(queue)
	m := aMessage()

	if err := outbox.Enqueue(t.Context(), m); err != nil {
		t.Fatalf("Enqueue() = %v", err)
	}
	if len(queue.inserted) != 1 || queue.inserted[0] != m {
		t.Fatalf("stored %v, want the message", queue.inserted)
	}
	select {
	case <-outbox.C():
	case <-time.After(time.Second):
		t.Fatal("the enqueue did not wake the sender")
	}
}

// A burst of enqueues asks for one extra run, and never blocks the request.
func TestWakesCoalesceAndNeverBlock(t *testing.T) {
	outbox := mailing.NewOutbox(&fakeQueue{})
	for range 100 {
		outbox.Wake()
	}
	if err := outbox.Enqueue(t.Context(), aMessage()); err != nil {
		t.Fatalf("Enqueue() = %v", err)
	}

	<-outbox.C()
	select {
	case <-outbox.C():
		t.Fatal("a burst of wakes queued more than one run")
	default:
	}
}

func TestAnInvalidMessageIsRefusedAndNotStored(t *testing.T) {
	queue := &fakeQueue{}
	outbox := mailing.NewOutbox(queue)
	m := aMessage()
	m.To = "not an address"

	if err := outbox.Enqueue(t.Context(), m); !errors.Is(err, mailing.ErrInvalidMessage) {
		t.Fatalf("Enqueue() = %v, want ErrInvalidMessage", err)
	}
	if len(queue.inserted) != 0 {
		t.Fatalf("an invalid message was stored: %v", queue.inserted)
	}
}

func TestAStorageFailureIsReturned(t *testing.T) {
	failure := errors.New("connection refused")
	outbox := mailing.NewOutbox(&fakeQueue{err: failure})

	if err := outbox.Enqueue(t.Context(), aMessage()); !errors.Is(err, failure) {
		t.Fatalf("Enqueue() = %v, want the storage failure", err)
	}
}

func TestWithMailOffNothingIsEnqueued(t *testing.T) {
	outbox := mailing.Disabled()

	if outbox.Enabled() {
		t.Error("Enabled() = true for the disabled outbox")
	}
	if err := outbox.Enqueue(context.Background(), aMessage()); !errors.Is(err, mailing.ErrDisabled) {
		t.Fatalf("Enqueue() = %v, want ErrDisabled", err)
	}
	outbox.Wake() // must not panic
	if !mailing.NewOutbox(&fakeQueue{}).Enabled() {
		t.Error("Enabled() = false for an outbox with a queue")
	}
}
