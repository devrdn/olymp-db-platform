package provisioning

import "github.com/google/uuid"

// Tender coalesces requests to run the pool tender soon into one pending
// wake, so a burst of events asks for at most one extra run. Losing the
// contest id is safe because each tend walks every live contest.
//
// Trigger never blocks, so request handlers (after commit) and the scheduler
// can call it without waiting on CREATE DATABASE.
type Tender struct {
	wake chan struct{}
}

// NewTender returns a Tender with nothing pending.
func NewTender() *Tender {
	return &Tender{wake: make(chan struct{}, 1)}
}

// Trigger asks for a tend soon because a contest's roster changed or it just
// started. The id is unused; it documents the call site.
func (t *Tender) Trigger(uuid.UUID) {
	select {
	case t.wake <- struct{}{}:
	default:
		// A wake is already queued; this one has nothing to add.
	}
}

// C is the channel a background loop selects on to learn a wake is pending.
func (t *Tender) C() <-chan struct{} {
	return t.wake
}
