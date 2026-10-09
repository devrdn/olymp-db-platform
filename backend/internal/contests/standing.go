package contests

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Refusals a Standing gives. A network the contest is not held on is
// ErrAddressNotAllowed (enrollment.go).
var (
	// ErrNotAParticipant covers both never registered and disqualified, so a
	// caller cannot probe whether an account is on the roster.
	ErrNotAParticipant = errors.New("not a participant of this contest")
	// ErrContestNotRunning is a contest not open to this participant now but
	// able to open later: a draft, a published contest not yet started, an
	// unknown status, an individual window not yet open, or timing data no
	// deadline can be computed from.
	ErrContestNotRunning = errors.New("the contest is not running")
	// ErrContestEnded is a contest finished or archived; it never opens again.
	// Kept apart from ErrContestNotRunning so a waiting participant is never
	// told the contest is over.
	ErrContestEnded = errors.New("the contest has ended")
	// ErrParticipantFinished is a finished registration.
	ErrParticipantFinished = errors.New("the participant has finished")
	// ErrDeadlinePassed is a participant whose deadline plus grace has gone
	// by, or, under individual timing, whose window closed before they started.
	//
	// Submit also returns it when the write itself lands at or after the
	// deadline, checked against the core database's clock in the same
	// statement (§8); that check, not the gate, is final.
	ErrDeadlinePassed = errors.New("the deadline for this contest has passed")
)

// Standing is where one participant stands in one contest at one instant. It
// is computed, never stored; the zero Standing refuses everything.
//
// MayAct covers play reads, the console, answers, the workspace and signals;
// MayWait covers the events channel; Over means their results may be shown.
// MayAct implies MayWait, Over implies neither, and Refusal names why MayAct
// is false.
type Standing struct {
	phase phase
	// addressRefused is a refusal rather than a permission so the zero
	// Standing refuses by its phase and names the contest, not the network.
	addressRefused bool
	// draft is a contest not out yet: over for nobody, whatever the
	// registration says.
	draft bool
}

// phase is where the participant is in the contest's life, address aside.
type phase int

const (
	// phaseNotRunning is the zero value, so an uncomputed Standing is closed.
	phaseNotRunning phase = iota
	// phaseWaiting is a published contest not started: the events channel only.
	phaseWaiting
	phaseOpen
	// phaseTimeUp is deadline plus grace gone by, or, unstarted under
	// individual timing, the contest's window closed.
	phaseTimeUp
	// phaseEnded is the contest finished or archived, for everybody.
	phaseEnded
	phaseFinished
	// phaseExcluded is a disqualified registration.
	phaseExcluded
)

// Gate is the participation rule bound to the installation's deadline grace
// (DEADLINE_GRACE), the allowance an already-working participant gets past
// their deadline. The composition root builds one and hands the same *Gate to
// every consumer, the scheduler included, so no two can disagree on the grace.
//
// The zero Gate has no grace; tests may use it.
type Gate struct {
	grace time.Duration
}

// NewGate returns the gate for the given grace. Zero means no allowance;
// config.Load supplies the default. It panics on a negative grace, which
// config.Load already refuses, so one here is a wiring bug.
func NewGate(grace time.Duration) *Gate {
	if grace < 0 {
		panic(fmt.Sprintf("contests: negative deadline grace %s", grace))
	}
	return &Gate{grace: grace}
}

// StandingOf is the participation rule: where participant stands in contest at
// now, from addr.
//
// Precedence: disqualified, then finished (neither ever changes back); then a
// contest finished or archived (over for everybody); published waits for its
// start; any status but running is not open. In a running contest:
//
//   - a participant whose clock has not started (ClockPending) may start
//     inside [starts_at, ends_at), with no grace: grace covers a request
//     already in flight, not more time to begin. A nil bound is open.
//   - everybody else may act while now is before Deadline plus grace, not at
//     that instant, because the database refuses a write at now() >= deadline.
//     No deadline at all fails closed.
//
// A fixed contest is open once its status is running, even before starts_at:
// the status, moved by an organiser or the scheduler, is what opens a shared
// window.
// The address is checked separately so Over never depends on it.
func (g *Gate) StandingOf(c Contest, p Participant, now time.Time, addr netip.Addr) Standing {
	return Standing{
		phase:          g.phaseOf(c, p, now),
		addressRefused: !c.AllowsAddress(addr),
		draft:          c.Status == StatusDraft,
	}
}

func (g *Gate) phaseOf(c Contest, p Participant, now time.Time) phase {
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
	if !now.Before(g.closesAt(deadline)) {
		return phaseTimeUp
	}
	return phaseOpen
}

// startPhase places a participant who has not started their clock. ends_at is
// the first instant they may no longer start, with no grace.
func startPhase(c Contest, now time.Time) phase {
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return phaseNotRunning
	}
	if c.EndsAt != nil && !now.Before(*c.EndsAt) {
		return phaseTimeUp
	}
	return phaseOpen
}

// closesAt is the only place the grace is added to a deadline, so the gate and
// Submit's write cannot disagree.
func (g *Gate) closesAt(deadline time.Time) time.Time {
	return deadline.Add(g.grace)
}

// MayAct reports whether the participant may act in the contest now.
func (s Standing) MayAct() bool {
	return s.phase == phaseOpen && !s.addressRefused
}

// MayWait reports whether the participant may hold the events channel open:
// whenever they may act, and while a published contest has not started.
func (s Standing) MayWait() bool {
	return s.MayAct() || (s.phase == phaseWaiting && !s.addressRefused)
}

// Over reports that the participant may never act in this contest again.
//
// A draft is over for nobody. Otherwise a disqualified or finished
// registration ends it, even in a published contest. The clock ends it only
// through a window that has run: the contest finished or archived, or their
// time up in a running contest; a published contest is never over by time.
// The address never matters.
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

// Refusal is nil exactly when MayAct, otherwise one of the gate's sentinels,
// unwrapped. The play screen stops for good on ErrContestEnded and keeps
// waiting on ErrContestNotRunning, so the two must stay distinct sentinels.
//
// Order: states that never change, then the address, then "not yet", since
// waiting will not help someone on the wrong network.
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
