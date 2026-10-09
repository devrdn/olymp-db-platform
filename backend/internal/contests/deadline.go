package contests

import "time"

// ClockPending reports a participant in an individual-timing contest who has
// not started their clock. Under fixed timing it is never pending.
func ClockPending(c Contest, p Participant) bool {
	return c.Timing == TimingIndividual && p.StartedAt == nil
}

// Deadline is the formula every timing check uses (docs/ARCHITECTURE.md §8):
// the moment a participant's window closes, without grace (Gate.closesAt adds
// it).
//
//   - fixed:      deadline = ends_at; started_at is analytics only.
//   - individual: deadline = LEAST(started_at + duration_min, ends_at).
//
// ok is false for an individual participant who has not started, or for data
// that breaks Contest.Validate's invariants. Callers must fail closed on it:
// no deadline means not open, never open without limit.
func Deadline(c Contest, p Participant) (deadline time.Time, ok bool) {
	switch c.Timing {
	case TimingFixed:
		if c.EndsAt == nil {
			return time.Time{}, false
		}
		return *c.EndsAt, true

	case TimingIndividual:
		if p.StartedAt == nil || c.DurationMin == nil || *c.DurationMin <= 0 {
			return time.Time{}, false
		}
		deadline = p.StartedAt.Add(time.Duration(*c.DurationMin) * time.Minute)
		if c.EndsAt != nil && c.EndsAt.Before(deadline) {
			deadline = *c.EndsAt
		}
		return deadline, true

	default:
		// An unknown timing is not read as fixed: no deadline.
		return time.Time{}, false
	}
}
