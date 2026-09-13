package leaderboard_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

func at(minute int) *time.Time {
	t := start.Add(time.Duration(minute) * time.Minute)
	return &t
}

func entry(login string, points int, last *time.Time) leaderboard.Entry {
	return leaderboard.Entry{Registration: uuid.New(), Login: login, Points: points, LastScoredAt: last}
}

func summary(rows []leaderboard.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Login + ":" + string(rune('0'+r.Place))
	}
	return out
}

// More points first; equal points go to whoever reached them earlier; the same
// score at the same moment shares a place, and the next place is skipped.
func TestRankByPointsBreaksTiesByWhenTheScoreWasReached(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("slow", 30, at(50)),
		entry("none-a", 0, nil),
		entry("fast", 30, at(20)),
		entry("third", 10, at(5)),
		entry("none-b", 0, nil),
	})

	got := summary(rows)
	want := []string{"fast:1", "slow:2", "third:3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want it to start %v", got, want)
		}
	}
	// Nobody who scored nothing is ahead of anybody else who scored nothing.
	if rows[3].Place != 4 || rows[4].Place != 4 {
		t.Errorf("places of the two without points = %d, %d, want 4, 4", rows[3].Place, rows[4].Place)
	}
}

func TestRankSharesAPlaceForTheSameScoreAtTheSameMoment(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("a", 20, at(10)), entry("b", 20, at(10)), entry("c", 5, at(1)),
	})
	if rows[0].Place != 1 || rows[1].Place != 1 || rows[2].Place != 3 {
		t.Errorf("places = %v, want 1, 1, 3", summary(rows))
	}
}

// Winner mode has exactly one place: whoever answered the final question
// first. Everybody else is listed by points and carries no place at all.
func TestRankInWinnerModePlacesOnlyTheFirstFinalAnswer(t *testing.T) {
	late := entry("late", 5, at(40))
	late.FinalAt = at(40)
	early := entry("early", 1, at(30))
	early.FinalAt = at(30)
	rich := entry("rich", 50, at(10))

	rows := leaderboard.Rank(contests.ScoringWinner, []leaderboard.Entry{late, rich, early})

	if rows[0].Login != "early" || rows[0].Place != 1 || !rows[0].Winner {
		t.Fatalf("first row = %+v, want early as the placed winner", rows[0])
	}
	if rows[1].Login != "rich" || rows[1].Place != 0 || rows[1].Winner {
		t.Errorf("second row = %+v, want rich, unplaced", rows[1])
	}
	if rows[2].Login != "late" || rows[2].Place != 0 || rows[2].Winner {
		t.Errorf("third row = %+v, want late, unplaced", rows[2])
	}
}

func TestRankInWinnerModeWithNoFinalAnswerPlacesNobody(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringWinner, []leaderboard.Entry{entry("a", 5, at(1)), entry("b", 9, at(2))})
	for _, r := range rows {
		if r.Place != 0 || r.Winner {
			t.Errorf("row %+v, want unplaced", r)
		}
	}
	if rows[0].Login != "b" {
		t.Errorf("order = %v, want points order", summary(rows))
	}
}

// The label follows the contest's choice, and a deleted account is never
// shown under a login that may since belong to somebody else.
func TestLabelFollowsTheContestsChoiceAndHidesADeletedAccount(t *testing.T) {
	row := leaderboard.Row{Entry: leaderboard.Entry{Login: "ivanov", FullName: "Ivan Ivanov"}}
	if got := row.Label(contests.LeaderboardNamesLogin); got != "ivanov" {
		t.Errorf("login label = %q", got)
	}
	if got := row.Label(contests.LeaderboardNamesFullName); got != "Ivan Ivanov" {
		t.Errorf("full name label = %q", got)
	}
	row.AccountDeleted = true
	if got := row.Label(contests.LeaderboardNamesLogin); got != "" {
		t.Errorf("deleted account label = %q, want empty", got)
	}
}
