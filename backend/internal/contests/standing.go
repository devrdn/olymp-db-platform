package contests

import (
	"errors"
	"net/netip"
	"time"
)

// What a participant meets at the gate (StandingOf): the one refusal a
// Standing gives when it does not let them act. The sixth, a network the
// contest is not held on, is ErrAddressNotAllowed (enrollment.go), which
// enrolment refuses for the same reason.
var (
	// ErrNotAParticipant covers never having registered and having been
	// disqualified alike: both mean this contest will not take anything from
	// this person, and distinguishing them to the caller would tell somebody
	// probing a contest whether an account is on its roster.
	ErrNotAParticipant = errors.New("not a participant of this contest")
	// ErrContestNotRunning is a contest that is not open to this participant
	// now, but may yet be: a draft (a published contest can be taken back to
	// one), a published contest that has not started, a status this build
	// does not know, an individual window that has not opened, or timing data
	// no deadline can be computed from. A contest that will never open again
	// is ErrContestEnded instead.
	ErrContestNotRunning = errors.New("the contest is not running")
	// ErrContestEnded is a contest finished or archived: over for everybody,
	// and it never opens again. Distinct from ErrContestNotRunning so that a
	// participant waiting for a contest that is merely not open yet is never
	// told it is over.
	ErrContestEnded = errors.New("the contest has ended")
	// ErrParticipantFinished is a registration that is finished. Their
	// answers are in; everything closing with them is the point of finishing.
	ErrParticipantFinished = errors.New("the participant has finished")
	// ErrDeadlinePassed is a participant whose own time is up: their deadline
	// plus the grace has gone by, or, under individual timing, the contest's
	// own window closed before they started.
	//
	// The same sentinel answers an answer that arrives after the deadline at
	// the moment of the write, checked against the core database's own clock
	// inside the same statement as the write (§8) — a second, authoritative
	// check, not a repeat of the gate: the two happen at different moments,
	// and only that one gets to be the last word on whether the write lands.
	ErrDeadlinePassed = errors.New("the deadline for this contest has passed")
)

// Standing is where one participant stands in one contest at one instant:
// computed, never stored. The zero Standing refuses everything.
//
// Three questions are asked of it, and they cannot contradict each other:
// MayAct (play reads, the console, answers, the workspace, signals), MayWait
// (the events channel), and Over (it is over for them: their results may be
// shown). Over implies neither of the others, MayAct implies MayWait, and
// Refusal names why MayAct is false.
type Standing struct {
	phase phase
	// addressRefused is the contest's network restriction refusing the
	// caller's address. Stored as a refusal rather than a permission so the
	// zero Standing, with nothing known about the address, still refuses by
	// its phase alone and names the contest rather than the network.
	addressRefused bool
	// draft is a contest that is not out yet: over for nobody, whatever a
	// registration in it says.
	draft bool
}

// phase is where the participant is in the contest's life, address aside.
type phase int

const (
	// phaseNotRunning is not open to them yet: a draft, a status this build
	// does not know, a window of their own that has not opened, or timing
	// data no deadline can be computed from. The zero value, so a Standing
	// nobody computed is closed.
	phaseNotRunning phase = iota
	// phaseWaiting is a published contest that has not started: they may
	// hold the events channel open for its start, and nothing else.
	phaseWaiting
	// phaseOpen is the contest running and their time not up.
	phaseOpen
	// phaseTimeUp is their own deadline plus grace gone by, or, unstarted
	// under individual timing, the contest's window closed before they began.
	phaseTimeUp
	// phaseEnded is the contest finished or archived, for everybody.
	phaseEnded
	// phaseFinished is their registration finished.
	phaseFinished
	// phaseExcluded is their registration disqualified.
	phaseExcluded
)

// StandingOf is the one participation rule: where participant stands in
// contest at now, from addr, with grace the network allowance an
// already-working participant is given past their deadline (DEADLINE_GRACE).
//
// The registration is read first, because neither status it can carry here
// ever changes back: disqualified, then finished. Then the contest: finished
// or archived is over for everybody; published waits for its start; anything
// but running is not open. A running contest is then a matter of the clock:
//
//   - a participant whose own clock has not started (ClockPending: individual
//     timing) may start inside the contest's own window, [starts_at,
//     ends_at), with no grace — grace is an allowance for a request already
//     on its way from somebody working, not more time to begin. A nil bound
//     is open on that side.
//   - everybody else may act while now is before their Deadline plus grace,
//     and not at that instant: the core database refuses an answer whose
//     write happens at now() >= deadline, and the gate must not admit what
//     the write would refuse. No deadline at all is broken timing data, and
//     fails closed.
//
// A fixed contest is open as soon as its status is running, before its
// starts_at too: the status is what an organiser or the scheduler moves to
// open a shared window.
//
// The address is checked against the contest's own network restriction
// separately from all of that, so that Over never depends on where the
// caller happens to be; Refusal decides which of the two to name.
func StandingOf(c Contest, p Participant, now time.Time, grace time.Duration, addr netip.Addr) Standing {
	return Standing{
		phase:          phaseOf(c, p, now, grace),
		addressRefused: !c.AllowsAddress(addr),
		draft:          c.Status == StatusDraft,
	}
}

// phaseOf is StandingOf's rule without the address.
func phaseOf(c Contest, p Participant, now time.Time, grace time.Duration) phase {
	switch {
	case p.Status == RegistrationDisqualified:
		return phaseExcluded
	case p.Status == RegistrationFinished:
		return phaseFinished
	case c.Ended():
		return phaseEnded
	case c.Status == StatusPublished:
		return phaseWaiting
	case c.Status != StatusRunning:
		return phaseNotRunning
	case ClockPending(c, p):
		return startPhase(c, now)
	}

	deadline, ok := Deadline(c, p)
	if !ok {
		return phaseNotRunning
	}
	if !now.Before(closesAt(deadline, grace)) {
		return phaseTimeUp
	}
	return phaseOpen
}

// startPhase is where a participant who has not started their own clock
// stands in a running contest: before its window, inside it, or too late to
// begin. ends_at is the first instant they may no longer start at, with no
// grace.
func startPhase(c Contest, now time.Time) phase {
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return phaseNotRunning
	}
	if c.EndsAt != nil && !now.Before(*c.EndsAt) {
		return phaseTimeUp
	}
	return phaseOpen
}

// closesAt is the instant an already-working participant may no longer act:
// their deadline plus the grace. The one place grace is added, so the gate
// and anything else that needs that instant cannot disagree about it.
func closesAt(deadline time.Time, grace time.Duration) time.Time {
	return deadline.Add(grace)
}

// MayAct reports whether the participant may act in the contest now: read
// its story, questions, log and schema, run queries, answer, use the
// workspace, send signals.
func (s Standing) MayAct() bool {
	return s.phase == phaseOpen && !s.addressRefused
}

// MayWait reports whether the participant may hold the events channel open:
// whenever they may act, and also while a published contest they are
// registered for has not started yet, so they can see it start.
func (s Standing) MayWait() bool {
	return s.MayAct() || (s.phase == phaseWaiting && !s.addressRefused)
}

// Over reports that the participant may never act in this contest again.
//
// A draft is over for nobody: a contest that is not out has nothing to be
// over. In any other status their registration ends it, a published contest
// included: disqualified or finished is over for them. The clock ends it only
// through a window that has actually run — the contest finished or archived,
// or their own time up in a running contest — so a published contest is never
// over by time, however late it is. It never depends on the address: walking
// out of the room does not end a contest.
func (s Standing) Over() bool {
	if s.draft {
		return false
	}
	switch s.phase {
	case phaseExcluded, phaseFinished, phaseEnded, phaseTimeUp:
		return true
	}
	return false
}

// Refusal is why the participant may not act: nil exactly when MayAct, and
// otherwise one of the gate's sentinels, unwrapped.
//
// A contest that has ended is ErrContestEnded and one that is merely not open
// now is ErrContestNotRunning: the play screen stops for good on the first
// and keeps waiting on the second, so the two must never share a sentinel.
//
// States that can never change come first, then the address, then "not yet":
// a participant whose time is up is told so from anywhere, and one on the
// wrong network is told that before being told to wait, since waiting will
// not help them.
func (s Standing) Refusal() error {
	switch s.phase {
	case phaseExcluded:
		return ErrNotAParticipant
	case phaseFinished:
		return ErrParticipantFinished
	case phaseEnded:
		return ErrContestEnded
	case phaseTimeUp:
		return ErrDeadlinePassed
	}
	if s.addressRefused {
		return ErrAddressNotAllowed
	}
	if s.phase == phaseOpen {
		return nil
	}
	return ErrContestNotRunning
}
