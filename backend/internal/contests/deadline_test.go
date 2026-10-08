package contests_test

import (
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

var deadlineBase = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func duration(min int) *int { return &min }

func at(t time.Time) *time.Time { return &t }

// A fixed contest shares one window: everybody's deadline is the contest's
// own end, and when a participant started (or whether they started at all)
// does not enter the formula at all.
func TestFixedTimingDeadlineIsTheContestsEnd(t *testing.T) {
	ends := deadlineBase.Add(2 * time.Hour)
	c := contests.Contest{Timing: contests.TimingFixed, EndsAt: at(ends)}
	p := contests.Participant{}

	deadline, ok := contests.Deadline(c, p)
	if !ok {
		t.Fatal("Deadline() ok = false, want true")
	}
	if !deadline.Equal(ends) {
		t.Errorf("deadline = %v, want %v", deadline, ends)
	}
}

// registrations.started_at is analytics only under fixed timing (§8): a
// participant who started early or late gets exactly the same deadline as
// everybody else.
func TestFixedTimingIgnoresWhenAParticipantStarted(t *testing.T) {
	ends := deadlineBase.Add(2 * time.Hour)
	early := deadlineBase.Add(-time.Hour)
	c := contests.Contest{Timing: contests.TimingFixed, EndsAt: at(ends)}
	p := contests.Participant{StartedAt: at(early)}

	deadline, ok := contests.Deadline(c, p)
	if !ok || !deadline.Equal(ends) {
		t.Errorf("deadline = %v, ok = %v, want %v, true", deadline, ok, ends)
	}
}

// The ordinary individual case: a participant's own window is their own
// start plus their own minutes.
func TestIndividualTimingDeadlineIsStartPlusDuration(t *testing.T) {
	started := deadlineBase
	c := contests.Contest{
		Timing: contests.TimingIndividual, DurationMin: duration(45),
		EndsAt: at(deadlineBase.Add(24 * time.Hour)),
	}
	p := contests.Participant{StartedAt: at(started)}

	deadline, ok := contests.Deadline(c, p)
	if !ok {
		t.Fatal("Deadline() ok = false, want true")
	}
	want := started.Add(45 * time.Minute)
	if !deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", deadline, want)
	}
}

// The formula is LEAST(started_at + duration_min, ends_at): a participant who
// starts late enough that their own minutes would run past the contest's own
// close is still cut off at ends_at, not given the extra time.
func TestIndividualTimingIsCappedByTheContestsEnd(t *testing.T) {
	ends := deadlineBase.Add(10 * time.Minute)
	startedLate := deadlineBase
	c := contests.Contest{
		Timing: contests.TimingIndividual, DurationMin: duration(60),
		EndsAt: at(ends),
	}
	p := contests.Participant{StartedAt: at(startedLate)}

	deadline, ok := contests.Deadline(c, p)
	if !ok {
		t.Fatal("Deadline() ok = false, want true")
	}
	if !deadline.Equal(ends) {
		t.Errorf("deadline = %v, want the contest's own end %v, not started_at+duration", deadline, ends)
	}
}

// An individual contest may run with no ends_at at all — the publish gate
// (CheckPublishable) requires starts_at for individual timing but only
// requires ends_at for fixed timing, so this state is reachable in
// production, not merely a type-level possibility. Nothing caps the
// participant's own window in that case.
func TestIndividualTimingWithNoContestEndUsesDurationAlone(t *testing.T) {
	started := deadlineBase
	c := contests.Contest{Timing: contests.TimingIndividual, DurationMin: duration(30)}
	p := contests.Participant{StartedAt: at(started)}

	deadline, ok := contests.Deadline(c, p)
	if !ok {
		t.Fatal("Deadline() ok = false, want true")
	}
	want := started.Add(30 * time.Minute)
	if !deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", deadline, want)
	}
}

// A participant who has not yet begun their individual session has nothing
// for the formula to add duration_min to. There is no deadline yet to compare
// a clock against, and the caller must not invent one.
func TestIndividualTimingWithNoStartHasNoDeadlineYet(t *testing.T) {
	c := contests.Contest{
		Timing: contests.TimingIndividual, DurationMin: duration(45),
		EndsAt: at(deadlineBase.Add(24 * time.Hour)),
	}
	p := contests.Participant{}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true for a participant who has not started, want false")
	}
}

// A fixed contest with no ends_at should not happen once running — the
// publish gate refuses it — but the column is nullable and the type does not
// forbid it, so a caller must fail closed rather than treat "no bound" as "no
// deadline ever".
func TestFixedTimingWithNoContestEndHasNoDeadline(t *testing.T) {
	c := contests.Contest{Timing: contests.TimingFixed}
	p := contests.Participant{}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true for a fixed contest with no ends_at, want false (fail closed)")
	}
}

// An individual contest that somehow lost its duration (violating the
// invariant Contest.Validate enforces at write time) must not be treated as
// unbounded either.
func TestIndividualTimingWithNoDurationHasNoDeadline(t *testing.T) {
	c := contests.Contest{Timing: contests.TimingIndividual}
	p := contests.Participant{StartedAt: at(deadlineBase)}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true with no duration_min, want false (fail closed)")
	}
}

// An unrecognised timing value — a zero-value Contest, or data from a future
// build this one does not understand — must not be silently read as either
// model. Fail closed, the same as the other unresolvable states above.
func TestUnknownTimingHasNoDeadline(t *testing.T) {
	c := contests.Contest{EndsAt: at(deadlineBase.Add(time.Hour))}
	p := contests.Participant{}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true for an unrecognised timing, want false (fail closed)")
	}
}
