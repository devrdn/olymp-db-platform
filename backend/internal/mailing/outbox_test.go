package mailing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/google/uuid"
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
	outbox := mailing.Disabled(&fakeQueue{})

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

// A caller inside a transaction wakes the sender once it has committed, so
// the batch itself wakes nobody.
func TestABatchIsStoredInOneCallAndWakesNobody(t *testing.T) {
	queue := &fakeQueue{}
	outbox := mailing.NewOutbox(queue)
	first, second := aMessage(), aMessage()
	second.To = "ion@usm.md"

	if err := outbox.EnqueueMany(t.Context(), []mailing.Message{first, second}); err != nil {
		t.Fatalf("EnqueueMany() = %v", err)
	}

	if queue.batches != 1 || len(queue.inserted) != 2 || queue.inserted[1] != second {
		t.Fatalf("stored %v in %d calls, want both in one", queue.inserted, queue.batches)
	}
	select {
	case <-outbox.C():
		t.Fatal("the batch woke the sender before its transaction committed")
	default:
	}
}

func TestABatchWithOneInvalidMessageStoresNothing(t *testing.T) {
	queue := &fakeQueue{}
	bad := aMessage()
	bad.Body = ""

	err := mailing.NewOutbox(queue).EnqueueMany(t.Context(), []mailing.Message{aMessage(), bad})

	if !errors.Is(err, mailing.ErrInvalidMessage) {
		t.Fatalf("EnqueueMany() = %v, want ErrInvalidMessage", err)
	}
	if len(queue.inserted) != 0 {
		t.Errorf("stored %v from a refused batch", queue.inserted)
	}
}

func TestABatchIsRefusedWithMailOff(t *testing.T) {
	queue := &fakeQueue{}

	err := mailing.Disabled(queue).EnqueueMany(t.Context(), []mailing.Message{aMessage()})

	if !errors.Is(err, mailing.ErrDisabled) || len(queue.inserted) != 0 {
		t.Fatalf("EnqueueMany() = %v, stored %v; want ErrDisabled and nothing", err, queue.inserted)
	}
}

func TestPendingMessagesAreAbandonedEvenWithMailOff(t *testing.T) {
	account := uuid.New()
	for name, outbox := range map[string]func(*fakeQueue) *mailing.Outbox{
		"mail on":  func(q *fakeQueue) *mailing.Outbox { return mailing.NewOutbox(q) },
		"mail off": func(q *fakeQueue) *mailing.Outbox { return mailing.Disabled(q) },
	} {
		t.Run(name, func(t *testing.T) {
			queue := &fakeQueue{}

			err := outbox(queue).AbandonPending(t.Context(), mailing.KindSignInDetails, []uuid.UUID{account}, "superseded")

			if err != nil {
				t.Fatalf("AbandonPending() = %v", err)
			}
			if len(queue.abandoned) != 1 || queue.abandoned[0].users[0] != account ||
				queue.abandoned[0].kind != mailing.KindSignInDetails || queue.abandoned[0].reason != "superseded" {
				t.Errorf("abandoned %+v, want the account's sign-in details", queue.abandoned)
			}
		})
	}
}

func TestAbandoningNoAccountsTouchesNothing(t *testing.T) {
	queue := &fakeQueue{err: errors.New("must not be called")}

	if err := mailing.NewOutbox(queue).AbandonPending(t.Context(), mailing.KindSignInDetails, nil, "superseded"); err != nil {
		t.Fatalf("AbandonPending() = %v, want nothing to do", err)
	}
	if err := mailing.Disabled(nil).AbandonPending(t.Context(), mailing.KindSignInDetails,
		[]uuid.UUID{uuid.New()}, "superseded"); err != nil {
		t.Fatalf("AbandonPending() without a queue = %v, want nothing to do", err)
	}
}
