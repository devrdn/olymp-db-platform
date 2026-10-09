package conteststest

import "github.com/google/uuid"

// PoolTrigger is an in-memory contests.PoolTrigger that records which
// contests it was asked to wake the pool tender for, and when.
type PoolTrigger struct {
	// Triggered lists every call in order, duplicates included; coalescing
	// is provisioning.Tender's job, not its callers'.
	Triggered []uuid.UUID
	uow       *UnitOfWork
	// TriggeredWhileOpen counts calls made with uow's transaction still open.
	// A trigger must fire after commit, so this must stay zero.
	TriggeredWhileOpen int
}

// NewPoolTrigger returns an empty fake. uow may be nil when a test does not
// care when the trigger fired.
func NewPoolTrigger(uow *UnitOfWork) *PoolTrigger { return &PoolTrigger{uow: uow} }

func (p *PoolTrigger) Trigger(contestID uuid.UUID) {
	p.Triggered = append(p.Triggered, contestID)
	if p.uow != nil && p.uow.Open {
		p.TriggeredWhileOpen++
	}
}
