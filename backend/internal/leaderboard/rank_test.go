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

func icpcEntry(login string, solved, penalty int, last *time.Time, cells ...leaderboard.Cell) leaderboard.Entry {
	return leaderboard.Entry{
		Registration: uuid.New(), Login: login, Solved: solved, Penalty: penalty,
		LastSolvedAt: last, Cells: cells,
	}
}

func solvedCell(at *time.Time) leaderboard.Cell { return leaderboard.Cell{SolvedAt: at} }

func grid(firstSolves ...*time.Time) leaderboard.Grid {
	return leaderboard.Grid{Questions: len(firstSolves), FirstSolves: firstSolves}
}

func firstMarks(rows []leaderboard.Row) map[string][]bool {
	marks := map[string][]bool{}
	for _, r := range rows {
		marks[r.Login] = []bool{}
		for _, c := range r.Cells {
			marks[r.Login] = append(marks[r.Login], c.First)
		}
	}
	return marks
}

func sameMarks(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ICPC: more solved first, then less penalty; the same solved and penalty
// share a place (1, 1, 3) whenever the last solve came. Points, which are
// zero in this mode, decide nothing even when a row carries some.
func TestRankICPCSharesAPlaceForEqualSolvedAndPenalty(t *testing.T) {
	fewer := icpcEntry("fewer", 1, 5, at(5))
	fewer.Points = 100

	rows := leaderboard.RankICPC([]leaderboard.Entry{
		icpcEntry("tie-late", 2, 50, at(40)),
		fewer,
		icpcEntry("tie-early", 2, 50, at(30)),
		icpcEntry("less-penalty", 2, 40, at(50)),
		icpcEntry("nothing", 0, 0, nil),
	}, grid())

	got := summary(rows)
	want := []string{"less-penalty:1", "tie-early:2", "tie-late:2", "fewer:4", "nothing:5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// A cell is first when its solve is the question's earliest solve as storage
// computed it over the whole contest — not the earliest among the rows at
// hand, which a row bound or the disqualified on the staff table would skew.
// A disqualified row is never first, even in the same instant.
func TestRankICPCMarksTheFirstSolverByTheContestsEarliestSolve(t *testing.T) {
	banned := icpcEntry("banned", 2, 31, at(30), solvedCell(at(1)), solvedCell(at(30)))
	banned.Disqualified = true
	early := icpcEntry("early", 2, 35, at(30), solvedCell(at(5)), solvedCell(at(30)))
	late := icpcEntry("late", 1, 10, at(10), solvedCell(at(10)), leaderboard.Cell{Wrong: 4})
	entries := []leaderboard.Entry{banned, late, early}

	// Somebody below the row bound solved A at minute 3, before anybody here.
	rows := leaderboard.RankICPC(entries, grid(at(3), at(30)))

	want := map[string][]bool{"banned": {false, false}, "early": {false, true}, "late": {false, false}}
	got := firstMarks(rows)
	for login, marks := range want {
		if !sameMarks(got[login], marks) {
			t.Errorf("%s first marks = %v, want %v", login, got[login], marks)
		}
	}
	// The mark is the ranking's, not storage's: the entries given are untouched.
	if entries[2].Cells[1].First {
		t.Error("RankICPC wrote the first mark into the entries it was given")
	}
}

// Two solves in the same instant are both first: neither was earlier.
func TestRankICPCMarksBothSolvesInTheSameInstantFirst(t *testing.T) {
	rows := leaderboard.RankICPC([]leaderboard.Entry{
		icpcEntry("a", 1, 7, at(7), solvedCell(at(7))),
		icpcEntry("b", 1, 7, at(7), solvedCell(at(7))),
		icpcEntry("c", 1, 8, at(8), solvedCell(at(8))),
	}, grid(at(7)))

	want := map[string][]bool{"a": {true}, "b": {true}, "c": {false}}
	got := firstMarks(rows)
	for login, marks := range want {
		if !sameMarks(got[login], marks) {
			t.Errorf("%s first marks = %v, want %v", login, got[login], marks)
		}
	}
}
