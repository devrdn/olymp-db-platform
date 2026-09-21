package profile_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

// enrol adds a contest of the given status to what the store answers with,
// and returns it.
func (r *rig) enrol(t *testing.T, status string) (contests.Contest, contests.Participant) {
	t.Helper()
	c, p := r.seed(t, status)
	r.store.enrolments = append(r.store.enrolments, profile.Enrolment{Contest: c, Participant: p})
	return c, p
}

func TestTheListReadsStorageOnceHoweverManyContests(t *testing.T) {
	r := newRig(t)
	for range 3 {
		c, _ := r.enrol(t, contests.StatusFinished)
		r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true,
			Scoring: contests.ScoringPoints, Place: 2, Participants: 7,
			Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 20, Solved: 2}}}
	}

	rows, truncated, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if len(rows) != 3 || truncated {
		t.Fatalf("Contests() returned %d rows, truncated %v", len(rows), truncated)
	}
	if r.store.reads["enrolments"] != 1 {
		t.Errorf("storage was read %d times, want one aggregating read", r.store.reads["enrolments"])
	}
	for _, row := range rows {
		if !row.Over || row.Result.Place != 2 || row.Result.Participants != 7 || row.Result.Points != 20 {
			t.Errorf("row = %+v, want the finished contest's own result and place", row)
		}
	}
}

// A running contest is a link and nothing else: no result, no place, and the
// leaderboard is not asked about it at all (design §1).
func TestARunningContestCarriesNoResult(t *testing.T) {
	r := newRig(t)
	r.now = start.Add(time.Hour)
	c, _ := r.enrol(t, contests.StatusRunning)
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateLive,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 20}}}

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if len(rows) != 1 || rows[0].Over || rows[0].Result.Points != 0 {
		t.Fatalf("row = %+v, want a running contest with no result", rows[0])
	}
	if r.results.asks != 0 {
		t.Errorf("the leaderboard was asked %d times about a running contest, want none", r.results.asks)
	}
}

// The freeze is not walked round here either: the participant's own numbers
// are shown, the place is not.
func TestAFrozenContestShowsTheOwnResultWithoutAPlace(t *testing.T) {
	r := newRig(t)
	c, _ := r.enrol(t, contests.StatusFinished)
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFrozen, Open: false,
		Scoring: contests.ScoringPoints, Row: leaderboard.Row{Entry: leaderboard.Entry{Points: 20, Solved: 2}}}

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	got := rows[0].Result
	if got.Points != 20 || got.Solved != 2 {
		t.Errorf("result = %+v, want the participant's own numbers", got)
	}
	if got.PlaceOpen || got.Place != 0 || got.Participants != 0 {
		t.Errorf("result = %+v, want no place while the table is frozen", got)
	}
	if got.State != leaderboard.StateFrozen {
		t.Errorf("state = %s, want frozen so the interface can say why", got.State)
	}
}

func TestTheListIsBounded(t *testing.T) {
	r := newRig(t)
	for range profile.MaxContests + 3 {
		c, p := r.seed(t, contests.StatusPublished)
		r.store.enrolments = append(r.store.enrolments, profile.Enrolment{Contest: c, Participant: p})
	}
	r.now = start.Add(-time.Hour)

	rows, truncated, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if len(rows) != profile.MaxContests || !truncated {
		t.Errorf("Contests() returned %d rows, truncated %v, want %d and truncated",
			len(rows), truncated, profile.MaxContests)
	}
	if r.store.lastLimit != profile.MaxContests+1 {
		t.Errorf("storage was asked for %d rows, want one past the bound so truncation is a fact",
			r.store.lastLimit)
	}
}

// A contest the leaderboard cannot place the caller on — their registration
// was made after a frozen table's cutoff, say — is still listed, without a
// result. One contest's missing number is not the whole list's failure.
func TestAContestWithNoStandingIsStillListed(t *testing.T) {
	r := newRig(t)
	r.enrol(t, contests.StatusFinished)

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if len(rows) != 1 || !rows[0].Over || rows[0].Result.Points != 0 {
		t.Errorf("row = %+v, want the contest listed with no result", rows[0])
	}
}

func TestTheListCarriesTheICPCResult(t *testing.T) {
	r := newRig(t)
	c, _ := r.enrol(t, contests.StatusFinished)
	c.Scoring = contests.ScoringICPC
	r.contests.Put(c)
	r.store.enrolments[0].Contest = c
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true,
		Scoring: contests.ScoringICPC, Place: 1, Participants: 4,
		Row: leaderboard.Row{Entry: leaderboard.Entry{Solved: 3, Penalty: 91}}}

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	got := rows[0].Result
	if got.Scoring != contests.ScoringICPC || got.Solved != 3 || got.Penalty != 91 || got.Place != 1 {
		t.Errorf("result = %+v, want the ICPC row", got)
	}
}

func TestTheListAsksForTheCallersOwnRegistration(t *testing.T) {
	r := newRig(t)
	c, p := r.enrol(t, contests.StatusFinished)
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true}
	var asked uuid.UUID
	r.service = profile.NewService(profile.Config{
		Store: r.store, Contests: r.contests, Participants: r.people, Attempts: r.attempts,
		Results: ownFunc(func(contestID, registration uuid.UUID) (leaderboard.Own, error) {
			asked = registration
			return r.results.own[contestID], nil
		}),
		Now: func() time.Time { return r.now },
	})

	if _, _, err := r.service.Contests(t.Context(), r.user); err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if asked != p.ID {
		t.Errorf("the leaderboard was asked about %s, want the caller's own registration %s", asked, p.ID)
	}
}
