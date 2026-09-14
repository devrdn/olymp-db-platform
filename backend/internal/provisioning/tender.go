package provisioning

import "github.com/google/uuid"

// Tender coalesces a request to run the pool tender again soon into a single
// pending wake, so a burst of registrations — or a scheduler tick that starts
// several contests at once — asks for at most one extra run rather than one
// per event.
//
// Trigger is meant to be called from a request handler after its own
// transaction has committed, or from the scheduler's own tick the same way:
// places that must never wait on CREATE DATABASE, and must never wait on each
// other either. It never blocks and never spawns a goroutine — a wake already
// pending (the channel's one slot is full) means somebody is already going to
// look, and Tend (background.go's tendPools) always walks every live contest
// in one pass regardless of which contest asked, so a coalesced wake tends
// every contest that asked exactly as well as one wake per contest would
// have.
type Tender struct {
	wake chan struct{}
}

// NewTender returns a Tender with nothing pending.
func NewTender() *Tender {
	return &Tender{wake: make(chan struct{}, 1)}
}

// Trigger asks for a tend soon because contestID's roster changed, or because
// it just started. The id is not otherwise used — Tend tends every live
// contest in one pass regardless of which one asked — but the parameter is
// what makes a call site read as "this contest's pool needs attention" rather
// than "the pool in general", the same as every other caller of this method.
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
