package profile_test

import (
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/profile"
)

func (r *rig) enrol(t *testing.T, status string, result profile.Result) (contests.Contest, contests.Participant) {
	t.Helper()
	c, p := r.seed(t, status)
	r.store.enrolments = append(r.store.enrolments,
		profile.Enrolment{Contest: c, Participant: p, Result: result})
	return c, p
}

func scored(points, solved int) profile.Result {
	return profile.Result{Scoring: contests.ScoringPoints, Points: points, Solved: solved}
}

func TestTheListIsOneReadHoweverManyContests(t *testing.T) {
	r := newRig(t)
	for range 3 {
		r.enrol(t, contests.StatusFinished, scored(20, 2))
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
		if !row.Over || row.Result.Points != 20 || row.Result.Solved != 2 {
			t.Errorf("row = %+v, want the finished contest's own result", row)
		}
	}
}

func TestTheListNamesNoPlaceAndAsksTheLeaderboardNothing(t *testing.T) {
	r := newRig(t)
	c, _ := r.enrol(t, contests.StatusFinished, scored(20, 2))
	r.results.own[c.ID] = leaderboard.Own{State: leaderboard.StateFinal, Open: true, Place: 2, Participants: 9}

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if r.results.asks != 0 {
		t.Errorf("the leaderboard was asked %d times for a list, want none", r.results.asks)
	}
	got := rows[0].Result
	if got.Place != 0 || got.Participants != 0 {
		t.Errorf("result = %+v, want no place in the list", got)
	}
	if !got.PlaceOpen || got.State != leaderboard.StateFinal {
		t.Errorf("result = %+v, want the final table reported as open", got)
	}
}

func TestARunningContestCarriesNoResult(t *testing.T) {
	r := newRig(t)
	r.now = start.Add(time.Hour)
	r.enrol(t, contests.StatusRunning, scored(20, 2))

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if len(rows) != 1 || rows[0].Over || rows[0].Result.Points != 0 || rows[0].Result.State != "" {
		t.Fatalf("row = %+v, want a running contest with no result", rows[0])
	}
}

func TestARunningContestCarriesNoResultUntilTheGraceHasGoneBy(t *testing.T) {
	for name, given := range map[string]struct {
		now      time.Time
		wantOver bool
	}{
		"at exactly the deadline": {now: end, wantOver: false},
		"at deadline+grace":       {now: end.Add(grace), wantOver: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.now = given.now
			r.enrol(t, contests.StatusRunning, scored(20, 2))

			rows, _, err := r.service.Contests(t.Context(), r.user)
			if err != nil {
				t.Fatalf("Contests() = %v", err)
			}
			if len(rows) != 1 {
				t.Fatalf("Contests() returned %d rows, want 1", len(rows))
			}
			if rows[0].Over != given.wantOver {
				t.Fatalf("Over = %v, want %v", rows[0].Over, given.wantOver)
			}
			if !given.wantOver && rows[0].Result.Points != 0 {
				t.Errorf("result = %+v, want none while the contest is still being taken", rows[0].Result)
			}
			if given.wantOver && rows[0].Result.Points != 20 {
				t.Errorf("result = %+v, want the participant's own 20 points", rows[0].Result)
			}
		})
	}
}

func TestAFrozenContestShowsTheOwnResultAndSaysTheTableIsShut(t *testing.T) {
	r := newRig(t)
	freeze := 30
	c, _ := r.enrol(t, contests.StatusFinished, scored(20, 2))
	c.LeaderboardFreezeMin = &freeze
	r.contests.Put(c)
	r.store.enrolments[0].Contest = c

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	got := rows[0].Result
	if got.Points != 20 || got.Solved != 2 {
		t.Errorf("result = %+v, want the participant's own numbers", got)
	}
	if got.PlaceOpen || got.State != leaderboard.StateFrozen {
		t.Errorf("result = %+v, want a frozen table that is not open", got)
	}
}

func TestARevealedFreezeOpensTheListsRow(t *testing.T) {
	r := newRig(t)
	freeze, revealed := 30, end.Add(time.Minute)
	c, _ := r.enrol(t, contests.StatusFinished, scored(20, 2))
	c.LeaderboardFreezeMin, c.LeaderboardRevealedAt = &freeze, &revealed
	r.contests.Put(c)
	r.store.enrolments[0].Contest = c

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	if got := rows[0].Result; !got.PlaceOpen || got.State != leaderboard.StateFinal {
		t.Errorf("result = %+v, want the revealed table reported as open", got)
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

func TestTheListCarriesTheICPCResult(t *testing.T) {
	r := newRig(t)
	c, _ := r.enrol(t, contests.StatusFinished,
		profile.Result{Scoring: contests.ScoringICPC, Solved: 3, Penalty: 91})
	c.Scoring = contests.ScoringICPC
	r.contests.Put(c)
	r.store.enrolments[0].Contest = c

	rows, _, err := r.service.Contests(t.Context(), r.user)
	if err != nil {
		t.Fatalf("Contests() = %v", err)
	}
	got := rows[0].Result
	if got.Scoring != contests.ScoringICPC || got.Solved != 3 || got.Penalty != 91 {
		t.Errorf("result = %+v, want the ICPC row", got)
	}
}
