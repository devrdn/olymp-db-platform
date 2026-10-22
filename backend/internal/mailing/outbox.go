package mailing

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Queue stores messages for the dispatcher. Every method joins the
// transaction ctx carries (storage.QuerierFrom), so a message lands, or is
// given up, with the change it reports or not at all.
type Queue interface {
	Insert(ctx context.Context, m Message) error
	// InsertMany stores the messages in one statement.
	InsertMany(ctx context.Context, ms []Message) error
	// AbandonPending gives up, with reason, every pending message of kind
	// for the accounts and erases its body, except one a sender is handing
	// to the relay at this moment.
	AbandonPending(ctx context.Context, kind Kind, users []uuid.UUID, reason string) error
}

// Outbox is where requests leave messages; the dispatcher delivers them.
type Outbox struct {
	// queue is nil only for an outbox with no table behind it.
	queue   Queue
	enabled bool
	wake    chan struct{}
}

// NewOutbox returns an outbox over queue.
func NewOutbox(queue Queue) *Outbox {
	return &Outbox{queue: queue, enabled: true, wake: make(chan struct{}, 1)}
}

// Disabled returns the outbox of an installation with mail off: every enqueue
// is refused with ErrDisabled. Messages queued before mail was turned off can
// still be abandoned through queue, which may be nil.
func Disabled(queue Queue) *Outbox {
	return &Outbox{queue: queue, wake: make(chan struct{}, 1)}
}

// Enabled reports whether messages can be queued.
func (o *Outbox) Enabled() bool { return o.enabled }

// Enqueue validates and stores m, inside the transaction ctx carries when
// there is one, and wakes the dispatcher. A message stored inside a
// transaction is invisible to the dispatcher until it commits, so a caller in
// a transaction calls Wake once it has committed; otherwise the message waits
// for the dispatcher's next tick.
func (o *Outbox) Enqueue(ctx context.Context, m Message) error {
	if !o.enabled {
		return ErrDisabled
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if err := o.queue.Insert(ctx, m); err != nil {
		return fmt.Errorf("queue a %s message: %w", m.Kind, err)
	}
	o.Wake()
	return nil
}

// EnqueueMany validates and stores the messages in one statement, all or
// none, and wakes nobody: it is for a caller inside a transaction, which calls
// Wake once it has committed.
func (o *Outbox) EnqueueMany(ctx context.Context, ms []Message) error {
	if !o.enabled {
		return ErrDisabled
	}
	if len(ms) == 0 {
		return nil
	}
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	if err := o.queue.InsertMany(ctx, ms); err != nil {
		return fmt.Errorf("queue %d messages: %w", len(ms), err)
	}
	return nil
}

// AbandonPending gives up the accounts' pending messages of kind, inside the
// transaction ctx carries, whether mail is on or not. Nothing was tried, so
// it counts no delivery outcome.
func (o *Outbox) AbandonPending(ctx context.Context, kind Kind, users []uuid.UUID, reason string) error {
	if o.queue == nil || len(users) == 0 {
		return nil
	}
	if err := o.queue.AbandonPending(ctx, kind, users, reason); err != nil {
		return fmt.Errorf("abandon pending %s messages: %w", kind, err)
	}
	return nil
}

// Wake asks the dispatcher for a run soon. It never blocks: a burst of wakes
// is one run, since each run drains everything due.
func (o *Outbox) Wake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// C is the channel the dispatcher's background job selects on.
func (o *Outbox) C() <-chan struct{} { return o.wake }
