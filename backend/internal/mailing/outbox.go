package mailing

import (
	"context"
	"fmt"
)

// Queue stores a message for the dispatcher. It joins the transaction ctx
// carries (storage.QuerierFrom), so a message lands with the change it
// reports or not at all.
type Queue interface {
	Insert(ctx context.Context, m Message) error
}

// Outbox is where requests leave messages; the dispatcher delivers them.
type Outbox struct {
	// queue is nil while mail is off.
	queue Queue
	wake  chan struct{}
}

// NewOutbox returns an outbox over queue.
func NewOutbox(queue Queue) *Outbox {
	return &Outbox{queue: queue, wake: make(chan struct{}, 1)}
}

// Disabled returns the outbox of an installation with mail off: every enqueue
// is refused with ErrDisabled.
func Disabled() *Outbox {
	return &Outbox{wake: make(chan struct{}, 1)}
}

// Enabled reports whether messages can be queued.
func (o *Outbox) Enabled() bool { return o.queue != nil }

// Enqueue validates and stores m, inside the transaction ctx carries when
// there is one, and wakes the dispatcher. A message stored inside a
// transaction is invisible to the dispatcher until it commits, so a caller in
// a transaction calls Wake once it has committed; otherwise the message waits
// for the dispatcher's next tick.
func (o *Outbox) Enqueue(ctx context.Context, m Message) error {
	if o.queue == nil {
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
