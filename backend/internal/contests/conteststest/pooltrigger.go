package conteststest

import "github.com/google/uuid"

// PoolTrigger is an in-memory contests.PoolTrigger, recording every contest it
// was asked to wake the pool tender for so a test can assert whether — and
// how often, and when — a write actually triggered one.
type PoolTrigger struct {
	// Triggered lists every contest id Trigger was called with, in call
	// order, duplicates included: the fake records what happened, and
	// whether that should have coalesced is provisioning.Tender's own claim
	// (proven in internal/provisioning), not this package's callers'.
	Triggered []uuid.UUID
	// uow, when set, is checked on every Trigger call: this is what lets a
	// test prove the trigger fired after the caller's own transaction
	// committed, never from inside it.
	uow *UnitOfWork
	// TriggeredWhileOpen counts how many Trigger calls landed while uow
	// reported a transaction still open. Every caller in this package fires
	// its trigger after uow.Do has already returned, so this must stay zero.
	TriggeredWhileOpen int
}

// NewPoolTrigger returns a fake with nothing triggered yet. uow may be nil
// for a test that only cares what was triggered, not when.
func NewPoolTrigger(uow *UnitOfWork) *PoolTrigger { return &PoolTrigger{uow: uow} }

func (p *PoolTrigger) Trigger(contestID uuid.UUID) {
	p.Triggered = append(p.Triggered, contestID)
	if p.uow != nil && p.uow.Open {
		p.TriggeredWhileOpen++
	}
}
