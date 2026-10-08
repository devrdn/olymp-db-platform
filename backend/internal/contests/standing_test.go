package contests_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

// The instants every row of the table is written against: a contest that
// runs from nine to noon, a participant's own hour when they start at ten,
// and the network allowance an already-working participant is given past
// their deadline.
var (
	standingStarts = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	standingEnds   = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	standingTen    = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	standingLab    = netip.MustParsePrefix("10.20.0.0/16")
	inTheLab       = netip.MustParseAddr("10.20.30.40")
	outsideTheLab  = netip.MustParseAddr("203.0.113.7")
)

const standingGrace = 5 * time.Second

// fixedContest is a fixed-timing contest in the given status, held on the lab
// network.
func fixedContest(status string) contests.Contest {
	return contests.Contest{
		Status: status, Timing: contests.TimingFixed,
		StartsAt: at(standingStarts), EndsAt: at(standingEnds),
		AllowedCIDRs: []netip.Prefix{standingLab},
	}
}

// individualContest is an individual-timing contest of sixty minutes per
// participant in the given status, held on the lab network.
func individualContest(status string) contests.Contest {
	c := fixedContest(status)
	c.Timing, c.DurationMin = contests.TimingIndividual, duration(60)
	return c
}

func registered() contests.Participant {
	return contests.Participant{Status: contests.RegistrationRegistered}
}

func startedAt(t time.Time) contests.Participant {
	return contests.Participant{Status: contests.RegistrationActive, StartedAt: at(t)}
}

func withStatus(p contests.Participant, status string) contests.Participant {
	p.Status = status
	return p
}

// standingRow is one participant in one contest at one instant, and what the
// gate must say about them. Every expectation is written out rather than
// derived, so the table reads as the rule.
type standingRow struct {
	name        string
	contest     contests.Contest
	participant contests.Participant
	now         time.Time
	addr        netip.Addr

	act, wait, over bool
	refusal         error
}

func standingRows() []standingRow {
	unrestricted := fixedContest(contests.StatusRunning)
	unrestricted.AllowedCIDRs = nil

	noFixedEnd := fixedContest(contests.StatusRunning)
	noFixedEnd.EndsAt = nil

	noIndividualEnd := individualContest(contests.StatusRunning)
	noIndividualEnd.EndsAt = nil

	noIndividualStart := individualContest(contests.StatusRunning)
	noIndividualStart.StartsAt = nil

	noDuration := individualContest(contests.StatusRunning)
	noDuration.DurationMin = nil

	unknownTiming := fixedContest(contests.StatusRunning)
	unknownTiming.Timing = "relay"

	running := contests.StatusRunning

	return []standingRow{
		// Fixed timing: everybody shares ends_at, and a working participant
		// keeps the grace past it.
		{name: "fixed, inside the window",
			contest: fixedContest(running), participant: registered(), now: standingTen, addr: inTheLab,
			act: true, wait: true},
		{name: "fixed, running before starts_at: the status is what opens a fixed contest",
			contest: fixedContest(running), participant: registered(), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			act: true, wait: true},
		{name: "fixed, at ends_at the grace still admits",
			contest: fixedContest(running), participant: registered(), now: standingEnds, addr: inTheLab,
			act: true, wait: true},
		{name: "fixed, one nanosecond before ends_at plus grace",
			contest: fixedContest(running), participant: registered(), now: standingEnds.Add(5*time.Second - time.Nanosecond), addr: inTheLab,
			act: true, wait: true},
		{name: "fixed, at exactly ends_at plus grace the time is up",
			contest: fixedContest(running), participant: registered(), now: standingEnds.Add(5 * time.Second), addr: inTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "fixed, time up is said before the address",
			contest: fixedContest(running), participant: registered(), now: standingEnds.Add(time.Hour), addr: outsideTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "fixed, from outside the allowed networks",
			contest: fixedContest(running), participant: registered(), now: standingTen, addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "fixed, an address nobody could name is refused under a restriction",
			contest: fixedContest(running), participant: registered(), now: standingTen, addr: netip.Addr{},
			refusal: contests.ErrAddressNotAllowed},
		{name: "fixed, no restriction admits an address nobody could name",
			contest: unrestricted, participant: registered(), now: standingTen, addr: netip.Addr{},
			act: true, wait: true},
		{name: "fixed, no ends_at is broken data and fails closed",
			contest: noFixedEnd, participant: registered(), now: standingTen, addr: inTheLab,
			refusal: contests.ErrContestNotRunning},
		{name: "fixed, broken data from outside the allowed networks names the address",
			contest: noFixedEnd, participant: registered(), now: standingTen, addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},

		// Individual timing, clock not started: the contest's own window,
		// [starts_at, ends_at), with no grace for starting.
		{name: "individual unstarted, one nanosecond before starts_at",
			contest: individualContest(running), participant: registered(), now: standingStarts.Add(-time.Nanosecond), addr: inTheLab,
			refusal: contests.ErrContestNotRunning},
		{name: "individual unstarted, before starts_at from outside the allowed networks",
			contest: individualContest(running), participant: registered(), now: standingStarts.Add(-time.Hour), addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "individual unstarted, at starts_at",
			contest: individualContest(running), participant: registered(), now: standingStarts, addr: inTheLab,
			act: true, wait: true},
		{name: "individual unstarted, one nanosecond before ends_at",
			contest: individualContest(running), participant: registered(), now: standingEnds.Add(-time.Nanosecond), addr: inTheLab,
			act: true, wait: true},
		{name: "individual unstarted, at exactly ends_at it is too late to start",
			contest: individualContest(running), participant: registered(), now: standingEnds, addr: inTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "individual unstarted, inside what would be the grace there is none for starting",
			contest: individualContest(running), participant: registered(), now: standingEnds.Add(4 * time.Second), addr: inTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "individual unstarted, at ends_at from outside the allowed networks the time is said first",
			contest: individualContest(running), participant: registered(), now: standingEnds, addr: outsideTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "individual unstarted, from outside the allowed networks inside the window",
			contest: individualContest(running), participant: registered(), now: standingTen, addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "individual unstarted, no ends_at never closes",
			contest: noIndividualEnd, participant: registered(), now: standingEnds.Add(1000 * time.Hour), addr: inTheLab,
			act: true, wait: true},
		{name: "individual unstarted, no starts_at opens at once",
			contest: noIndividualStart, participant: registered(), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			act: true, wait: true},

		// Individual timing, clock started: their own deadline plus grace.
		{name: "individual started at ten, inside their hour",
			contest: individualContest(running), participant: startedAt(standingTen), now: standingTen.Add(59 * time.Minute), addr: inTheLab,
			act: true, wait: true},
		{name: "individual started at ten, one nanosecond before eleven plus grace",
			contest: individualContest(running), participant: startedAt(standingTen), now: standingTen.Add(time.Hour + 5*time.Second - time.Nanosecond), addr: inTheLab,
			act: true, wait: true},
		{name: "individual started at ten, at exactly eleven plus grace the time is up",
			contest: individualContest(running), participant: startedAt(standingTen), now: standingTen.Add(time.Hour + 5*time.Second), addr: inTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "individual started at half past eleven, one nanosecond before ends_at plus grace",
			contest: individualContest(running), participant: startedAt(standingEnds.Add(-30 * time.Minute)), now: standingEnds.Add(5*time.Second - time.Nanosecond), addr: inTheLab,
			act: true, wait: true},
		{name: "individual started at half past eleven, capped at ends_at plus grace",
			contest: individualContest(running), participant: startedAt(standingEnds.Add(-30 * time.Minute)), now: standingEnds.Add(5 * time.Second), addr: inTheLab,
			over: true, refusal: contests.ErrDeadlinePassed},
		{name: "individual started, no duration is broken data and fails closed",
			contest: noDuration, participant: startedAt(standingTen), now: standingTen.Add(time.Minute), addr: inTheLab,
			refusal: contests.ErrContestNotRunning},
		{name: "individual started, broken data from outside the allowed networks names the address",
			contest: noDuration, participant: startedAt(standingTen), now: standingTen.Add(time.Minute), addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "a timing this build does not know fails closed",
			contest: unknownTiming, participant: registered(), now: standingTen, addr: inTheLab,
			refusal: contests.ErrContestNotRunning},

		// The contest's own status.
		{name: "draft",
			contest: fixedContest(contests.StatusDraft), participant: registered(), now: standingTen, addr: inTheLab,
			refusal: contests.ErrContestNotRunning},
		{name: "draft from outside the allowed networks",
			contest: fixedContest(contests.StatusDraft), participant: registered(), now: standingTen, addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "published: may wait, may not act",
			contest: fixedContest(contests.StatusPublished), participant: registered(), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			wait: true, refusal: contests.ErrContestNotRunning},
		{name: "published, still waiting past ends_at while the status has not moved",
			contest: fixedContest(contests.StatusPublished), participant: registered(), now: standingEnds.Add(time.Hour), addr: inTheLab,
			wait: true, refusal: contests.ErrContestNotRunning},
		{name: "published from outside the allowed networks may not even wait",
			contest: fixedContest(contests.StatusPublished), participant: registered(), now: standingStarts.Add(-time.Hour), addr: outsideTheLab,
			refusal: contests.ErrAddressNotAllowed},
		{name: "a status this build does not know",
			contest: fixedContest("paused"), participant: registered(), now: standingTen, addr: inTheLab,
			refusal: contests.ErrContestNotRunning},
		// An ended contest is named as ended, not as "not running": the one
		// will never open again, the other may, and the play screen has to
		// tell a participant which.
		{name: "finished",
			contest: fixedContest(contests.StatusFinished), participant: registered(), now: standingTen, addr: inTheLab,
			over: true, refusal: contests.ErrContestEnded},
		{name: "finished, said before the address",
			contest: fixedContest(contests.StatusFinished), participant: registered(), now: standingTen, addr: outsideTheLab,
			over: true, refusal: contests.ErrContestEnded},
		{name: "archived",
			contest: individualContest(contests.StatusArchived), participant: startedAt(standingTen), now: standingTen, addr: inTheLab,
			over: true, refusal: contests.ErrContestEnded},
		{name: "archived without ever running, before its own starts_at",
			contest: fixedContest(contests.StatusArchived), participant: registered(), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			over: true, refusal: contests.ErrContestEnded},

		// The registration's own status comes before everything else.
		{name: "disqualified inside the window",
			contest: fixedContest(running), participant: withStatus(registered(), contests.RegistrationDisqualified), now: standingTen, addr: inTheLab,
			over: true, refusal: contests.ErrNotAParticipant},
		{name: "disqualified, said before the contest ending and the address",
			contest: fixedContest(contests.StatusFinished), participant: withStatus(registered(), contests.RegistrationDisqualified), now: standingTen, addr: outsideTheLab,
			over: true, refusal: contests.ErrNotAParticipant},
		{name: "disqualified from a published contest may not wait, and it is over for them already",
			contest: fixedContest(contests.StatusPublished), participant: withStatus(registered(), contests.RegistrationDisqualified), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			over: true, refusal: contests.ErrNotAParticipant},
		{name: "disqualified from a draft: a contest that is not out is over for nobody",
			contest: fixedContest(contests.StatusDraft), participant: withStatus(registered(), contests.RegistrationDisqualified), now: standingTen, addr: inTheLab,
			refusal: contests.ErrNotAParticipant},
		{name: "registration finished in a draft: a contest that is not out is over for nobody",
			contest: fixedContest(contests.StatusDraft), participant: withStatus(registered(), contests.RegistrationFinished), now: standingTen, addr: inTheLab,
			refusal: contests.ErrParticipantFinished},
		{name: "registration finished inside the window",
			contest: individualContest(running), participant: withStatus(startedAt(standingTen), contests.RegistrationFinished), now: standingTen.Add(time.Minute), addr: inTheLab,
			over: true, refusal: contests.ErrParticipantFinished},
		{name: "registration finished in a published contest, over for them already",
			contest: fixedContest(contests.StatusPublished), participant: withStatus(registered(), contests.RegistrationFinished), now: standingStarts.Add(-time.Hour), addr: inTheLab,
			over: true, refusal: contests.ErrParticipantFinished},
		{name: "registration finished, said before the contest ending",
			contest: fixedContest(contests.StatusFinished), participant: withStatus(registered(), contests.RegistrationFinished), now: standingEnds.Add(time.Hour), addr: inTheLab,
			over: true, refusal: contests.ErrParticipantFinished},
	}
}

// Every boundary of the one participation rule, at the instant either side
// of it.
func TestStandingOf(t *testing.T) {
	for _, row := range standingRows() {
		t.Run(row.name, func(t *testing.T) {
			s := contests.StandingOf(row.contest, row.participant, row.now, standingGrace, row.addr)
			if got := s.MayAct(); got != row.act {
				t.Errorf("MayAct() = %v, want %v", got, row.act)
			}
			if got := s.MayWait(); got != row.wait {
				t.Errorf("MayWait() = %v, want %v", got, row.wait)
			}
			if got := s.Over(); got != row.over {
				t.Errorf("Over() = %v, want %v", got, row.over)
			}
			// The sentinel itself, not something wrapping it: the HTTP layer
			// answers each from a row of its own.
			if got := s.Refusal(); got != row.refusal {
				t.Errorf("Refusal() = %v, want %v", got, row.refusal)
			}
		})
	}
}

// The three answers can never contradict each other, whatever the inputs: a
// participant for whom it is over may neither act nor wait, one who may act
// may wait, and a refusal is given exactly when acting is not allowed.
func TestStandingAnswersAgreeWithEachOther(t *testing.T) {
	for _, row := range standingRows() {
		t.Run(row.name, func(t *testing.T) {
			s := contests.StandingOf(row.contest, row.participant, row.now, standingGrace, row.addr)
			if s.Over() && (s.MayAct() || s.MayWait()) {
				t.Errorf("Over() but MayAct() = %v, MayWait() = %v", s.MayAct(), s.MayWait())
			}
			if s.MayAct() && !s.MayWait() {
				t.Error("MayAct() but not MayWait()")
			}
			if (s.Refusal() == nil) != s.MayAct() {
				t.Errorf("Refusal() = %v while MayAct() = %v", s.Refusal(), s.MayAct())
			}
		})
	}
}

// A Standing nobody computed refuses everything, and does not claim the
// contest is over for anybody.
func TestTheZeroStandingRefusesEverything(t *testing.T) {
	var s contests.Standing
	if s.MayAct() || s.MayWait() || s.Over() {
		t.Errorf("zero Standing: MayAct() = %v, MayWait() = %v, Over() = %v, want all false",
			s.MayAct(), s.MayWait(), s.Over())
	}
	if got := s.Refusal(); got != contests.ErrContestNotRunning {
		t.Errorf("zero Standing: Refusal() = %v, want ErrContestNotRunning", got)
	}
}
