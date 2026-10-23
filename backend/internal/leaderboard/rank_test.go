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

func TestRankRefusesICPC(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Rank(icpc) did not panic")
		}
	}()
	leaderboard.Rank(contests.ScoringICPC, []leaderboard.Entry{entry("alice", 0, nil)})
}

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

func TestRankICPCMarksTheFirstSolverByTheContestsEarliestSolve(t *testing.T) {
	banned := icpcEntry("banned", 2, 31, at(30), solvedCell(at(1)), solvedCell(at(30)))
	banned.Disqualified = true
	early := icpcEntry("early", 2, 35, at(30), solvedCell(at(5)), solvedCell(at(30)))
	late := icpcEntry("late", 1, 10, at(10), solvedCell(at(10)), leaderboard.Cell{Wrong: 4})
	entries := []leaderboard.Entry{banned, late, early}

	rows := leaderboard.RankICPC(entries, grid(at(3), at(30)))

	want := map[string][]bool{"banned": {false, false}, "early": {false, true}, "late": {false, false}}
	got := firstMarks(rows)
	for login, marks := range want {
		if !sameMarks(got[login], marks) {
			t.Errorf("%s first marks = %v, want %v", login, got[login], marks)
		}
	}
	if entries[2].Cells[1].First {
		t.Error("RankICPC wrote the first mark into the entries it was given")
	}
}

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

func prizes(rows []leaderboard.Row) map[string]int {
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.Login] = r.Prize
	}
	return out
}

func samePrizes(t *testing.T, rows []leaderboard.Row, want map[string]int) {
	t.Helper()
	got := prizes(rows)
	for login, tier := range want {
		if got[login] != tier {
			t.Errorf("prizes = %v, want %v", got, want)
			return
		}
	}
}

func TestPrizesGoDownThePointsTable(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("a", 50, at(1)), entry("b", 40, at(1)), entry("c", 30, at(1)),
		entry("d", 20, at(1)), entry("e", 10, at(1)),
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1, Third: 2})

	samePrizes(t, rows, map[string]int{"a": 1, "b": 2, "c": 3, "d": 3, "e": 0})
}

// A shared place is shared whole: a tie on a boundary takes the better tier,
// and the place after it still counts the rows above.
func TestATieOnEachBoundaryTakesTheBetterTier(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("i-a", 50, at(1)), entry("i-b", 50, at(1)), // place 1, straddles I/II
		entry("ii-a", 40, at(1)), entry("ii-b", 40, at(1)), // place 3, straddles II/III
		entry("iii-a", 30, at(1)), entry("iii-b", 30, at(1)), // place 5, straddles III/none
		entry("none", 20, at(1)), // place 7
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 3, Third: 1})

	samePrizes(t, rows, map[string]int{
		"i-a": 1, "i-b": 1, "ii-a": 2, "ii-b": 2, "iii-a": 3, "iii-b": 3, "none": 0,
	})
}

func TestNoPrizePlacesAwardNothing(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("a", 50, at(1)), entry("b", 40, at(1)),
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{})

	samePrizes(t, rows, map[string]int{"a": 0, "b": 0})
}

// ICPC's places come from solved, then penalty, so a tie there is shared too.
func TestPrizesFollowICPCPlaces(t *testing.T) {
	solved := func(login string, n, penalty int) leaderboard.Entry {
		return leaderboard.Entry{Registration: uuid.New(), Login: login, Solved: n, Penalty: penalty}
	}
	rows := leaderboard.RankICPC([]leaderboard.Entry{
		solved("slow", 3, 90), solved("fast", 3, 40), solved("tied", 3, 90), solved("two", 2, 10),
	}, leaderboard.Grid{})

	leaderboard.AwardPrizes(rows, contests.ScoringICPC, contests.PrizePlaces{First: 1, Second: 1})

	samePrizes(t, rows, map[string]int{"fast": 1, "slow": 2, "tied": 2, "two": 0})
}

// Winner mode ignores the counts; its one winner holds the first prize.
func TestWinnerModeGivesTheWinnerTheFirstPrizeAndNobodyElseAny(t *testing.T) {
	final := entry("winner", 5, at(30))
	final.FinalAt = at(30)
	rows := leaderboard.Rank(contests.ScoringWinner, []leaderboard.Entry{final, entry("rich", 50, at(10))})

	leaderboard.AwardPrizes(rows, contests.ScoringWinner, contests.PrizePlaces{First: 5, Second: 5, Third: 5})

	samePrizes(t, rows, map[string]int{"winner": 1, "rich": 0})
	if !rows[0].Winner || rows[0].Place != 1 {
		t.Errorf("first row = %+v, want the winner still placed first", rows[0])
	}
}

// The staff table can mark a disqualified row the winner; it still takes no
// prize.
func TestADisqualifiedWinnerTakesNoPrize(t *testing.T) {
	cheat := entry("cheat", 5, at(10))
	cheat.FinalAt, cheat.Disqualified = at(10), true
	rows := leaderboard.Rank(contests.ScoringWinner, []leaderboard.Entry{cheat})

	leaderboard.AwardPrizes(rows, contests.ScoringWinner, contests.PrizePlaces{})

	samePrizes(t, rows, map[string]int{"cheat": 0})
}

func TestWinnerModeWithoutAWinnerGivesNoPrize(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringWinner, []leaderboard.Entry{entry("rich", 50, at(10))})

	leaderboard.AwardPrizes(rows, contests.ScoringWinner, contests.PrizePlaces{First: 1})

	samePrizes(t, rows, map[string]int{"rich": 0})
}

func TestAnAllZeroTableAwardsNothing(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("a", 0, nil), entry("b", 0, nil), entry("c", 0, nil),
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1, Third: 1})

	samePrizes(t, rows, map[string]int{"a": 0, "b": 0, "c": 0})
}

// Counts reaching past the last scorer stop at it: the zero scorers share the
// next place, inside the third-prize count, and still take nothing.
func TestZeroScorersInsideTheCountsTakeNoPrize(t *testing.T) {
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		entry("a", 30, at(1)), entry("b", 20, at(1)), entry("idle-1", 0, nil), entry("idle-2", 0, nil),
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1, Third: 5})

	samePrizes(t, rows, map[string]int{"a": 1, "b": 2, "idle-1": 0, "idle-2": 0})
}

func TestICPCRowsThatSolvedNothingTakeNoPrize(t *testing.T) {
	solved := func(login string, n, penalty int) leaderboard.Entry {
		return leaderboard.Entry{Registration: uuid.New(), Login: login, Solved: n, Penalty: penalty}
	}
	rows := leaderboard.RankICPC([]leaderboard.Entry{
		solved("one", 1, 30), solved("none-a", 0, 0), solved("none-b", 0, 0),
	}, leaderboard.Grid{})

	leaderboard.AwardPrizes(rows, contests.ScoringICPC, contests.PrizePlaces{First: 3})

	samePrizes(t, rows, map[string]int{"one": 1, "none-a": 0, "none-b": 0})
}

func TestADisqualifiedRowTakesNoPrizeAndHoldsNoPlaceForOne(t *testing.T) {
	cheat := entry("cheat", 90, at(1))
	cheat.Disqualified = true
	rows := leaderboard.Rank(contests.ScoringPoints, []leaderboard.Entry{
		cheat, entry("a", 50, at(1)), entry("b", 50, at(1)), entry("c", 40, at(1)), entry("d", 30, at(1)),
	})

	leaderboard.AwardPrizes(rows, contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1, Third: 1})

	samePrizes(t, rows, map[string]int{"cheat": 0, "a": 1, "b": 1, "c": 3, "d": 0})
}
