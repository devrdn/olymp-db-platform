package conteststest

import "github.com/google/uuid"

// PoolTrigger is an in-memory contests.PoolTrigger, recording every contest it
// was asked to wake the pool tender for so a test can assert whether — and
// how often — a write actually triggered one.
type PoolTrigger struct {
	// Triggered lists every contest id Trigger was called with, in call
	// order, duplicates included: the fake records what happened, and
	// whether that should have coalesced is provisioning.Tender's own claim
	// (proven in internal/provisioning), not this package's callers'.
	Triggered []uuid.UUID
}

// NewPoolTrigger returns a fake with nothing triggered yet.
func NewPoolTrigger() *PoolTrigger { return &PoolTrigger{} }

func (p *PoolTrigger) Trigger(contestID uuid.UUID) {
	p.Triggered = append(p.Triggered, contestID)
}
