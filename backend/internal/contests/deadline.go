package contests

import "time"

// ClockPending reports a participant whose own clock has not started: an
// individual-timing contest they have not yet begun. Under fixed timing
// nobody has a clock of their own to start, so it is never pending.
//
// A fact about the pair, beside Deadline for the same reason Deadline is a
// plain function: every path that asks already holds both values.
func ClockPending(c Contest, p Participant) bool {
	return c.Timing == TimingIndividual && p.StartedAt == nil
}

// Deadline is the one formula every timing check in the system uses
// (docs/ARCHITECTURE.md §8): the moment a participant's window in this
// contest closes.
//
//   - fixed:      deadline = ends_at. Everybody shares one window;
//     registration.StartedAt is analytics only and never enters the formula.
//   - individual: deadline = LEAST(started_at + duration_min, ends_at). A
//     participant may begin anywhere in [starts_at, ends_at] and gets their
//     own minutes from their own start, capped by the contest's own end.
//
// ok is false when no deadline can be produced: an individual-timing
// participant who has not started yet (there is nothing for duration_min to
// be added to), or a state that violates the invariants Contest.Validate
// enforces at write time (an unset duration_min, an unset ends_at on fixed
// timing, or a timing value this build does not recognise). Every one of
// those is a "fail closed" case rather than "no deadline ever": a caller
// with no deadline to compare a clock against must treat the contest as not
// currently open to the participant, never as open without limit.
//
// A plain function over values the caller already holds, not a method that
// fetches: the submission path, queryproxy and SSE each already have their
// own Contest and Participant in hand by the time they need this, and a
// second lookup here would just be a second place the timing rule could
// drift from this one.
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
		// An unrecognised timing is not "fixed" by default — that would read
		// data this build does not understand as a rule it does understand,
		// silently. There is no other logic to fall back to (§8): if this is
		// not one of the two models, there is no deadline.
		return time.Time{}, false
	}
}

// OpenForStart reports whether now falls inside the contest's own window —
// [starts_at, ends_at] — the wall-clock fact an individual participant's
// first action must satisfy before it may ever write registrations.started_at
// (§8, finding 1).
//
// Deliberately independent of Contest.Status: status is a manual step in an
// organiser's own workflow and can be moved to "running" hours before
// starts_at, or left at "running" long after ends_at by a scheduler that
// never ticked. Neither says anything about the wall clock, and queryproxy
// already has its own status check for what status alone is good for — this
// answers a different question, and is checked in addition to it, not instead
// of it.
//
// A nil bound is open on that side: starts_at unset means the contest opens
// immediately, ends_at unset means it never closes on its own. This mirrors
// what CheckPublishable already tolerates — an individual contest must have a
// starts_at to publish, but not an ends_at — so both are reachable in
// production, not merely type-level possibilities.
func (c Contest) OpenForStart(now time.Time) bool {
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return false
	}
	if c.EndsAt != nil && now.After(*c.EndsAt) {
		return false
	}
	return true
}
