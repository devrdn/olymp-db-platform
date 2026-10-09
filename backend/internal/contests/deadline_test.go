package contests_test

import (
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

var deadlineBase = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func duration(min int) *int { return &min }

func at(t time.Time) *time.Time { return &t }

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

// Under fixed timing started_at is analytics only (§8).
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

// The formula is LEAST(started_at + duration_min, ends_at).
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

// With no ends_at nothing caps the participant's own window.
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

// The publish gate refuses this, but the column is nullable: fail closed
// rather than read "no bound" as "no deadline ever".
func TestFixedTimingWithNoContestEndHasNoDeadline(t *testing.T) {
	c := contests.Contest{Timing: contests.TimingFixed}
	p := contests.Participant{}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true for a fixed contest with no ends_at, want false (fail closed)")
	}
}

func TestIndividualTimingWithNoDurationHasNoDeadline(t *testing.T) {
	c := contests.Contest{Timing: contests.TimingIndividual}
	p := contests.Participant{StartedAt: at(deadlineBase)}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true with no duration_min, want false (fail closed)")
	}
}

// A zero-value Contest or a timing from a newer build fails closed.
func TestUnknownTimingHasNoDeadline(t *testing.T) {
	c := contests.Contest{EndsAt: at(deadlineBase.Add(time.Hour))}
	p := contests.Participant{}

	if _, ok := contests.Deadline(c, p); ok {
		t.Error("Deadline() ok = true for an unrecognised timing, want false (fail closed)")
	}
}
