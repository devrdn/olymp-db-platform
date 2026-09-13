package leaderboard

import (
	"bytes"
	"slices"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

// Rank orders entries and gives them places under the contest's scoring mode.
//
// Points: more points first, then whoever reached their score earlier, with
// the same score at the same moment sharing a place (1, 1, 3). Nobody who
// scored nothing is ahead of anybody else who scored nothing.
//
// Winner: one place, for the earliest correct final answer; everybody else is
// listed in points order without a place, because "there is only a winner"
// (§6.1.1) is exactly one place.
//
// ICPC is ranked by RankICPC, which needs the grid storage computed beside
// the entries. Rank panics when given it: that is a programmer error, and
// ranking an ICPC table by points would quietly serve a table of zeros.
//
// The registration id is the last key only so that the order of equal rows
// does not change between two reads; a place never depends on it.
func Rank(scoring string, entries []Entry) []Row {
	if scoring == contests.ScoringICPC {
		panic("leaderboard: Rank called for icpc scoring; use RankICPC")
	}
	rows := make([]Row, len(entries))
	for i, e := range entries {
		rows[i] = Row{Entry: e}
	}
	slices.SortStableFunc(rows, func(a, b Row) int { return comparePoints(a.Entry, b.Entry) })

	if scoring == contests.ScoringWinner {
		// The winner moves to the top; everybody else keeps points order.
		winner := -1
		for i, r := range rows {
			if r.FinalAt != nil && (winner < 0 || compareFinal(r.Entry, rows[winner].Entry) < 0) {
				winner = i
			}
		}
		if winner >= 0 {
			w := rows[winner]
			w.Place, w.Winner = 1, true
			copy(rows[1:winner+1], rows[:winner])
			rows[0] = w
		}
		return rows
	}

	for i := range rows {
		if i > 0 && sameResult(rows[i-1].Entry, rows[i].Entry) {
			rows[i].Place = rows[i-1].Place
			continue
		}
		rows[i].Place = i + 1
	}
	return rows
}

// RankICPC orders ICPC entries and gives them places: more questions solved
// first, then less penalty time, the two equal sharing a place (1, 1, 3).
// Rows sharing a place are listed by their last solve, then by registration
// id, only for a stable order. Points, zero in this mode, are not read.
//
// A cell is marked first when it is solved at exactly the moment the grid
// names as its question's earliest solve, and its row is not disqualified.
// The moment comes from storage, computed over every registration that is not
// disqualified — never from the rows at hand, which the row bound may have
// cut the real first solver from, and which on the staff table include the
// disqualified. Two solves in the same instant are both first: neither was
// earlier. The cells are cut off already, so a frozen table marks by the
// answers before the freeze only.
func RankICPC(entries []Entry, grid Grid) []Row {
	rows := make([]Row, len(entries))
	for i, e := range entries {
		rows[i] = Row{Entry: e}
		// Copied, so the mark is never written into the entries given.
		rows[i].Cells = slices.Clone(e.Cells)
		if e.Disqualified {
			continue
		}
		for q := range rows[i].Cells {
			cell := &rows[i].Cells[q]
			cell.First = cell.SolvedAt != nil && q < len(grid.FirstSolves) &&
				grid.FirstSolves[q] != nil && cell.SolvedAt.Equal(*grid.FirstSolves[q])
		}
	}

	slices.SortStableFunc(rows, func(a, b Row) int { return compareICPC(a.Entry, b.Entry) })
	for i := range rows {
		if i > 0 && rows[i-1].Solved == rows[i].Solved && rows[i-1].Penalty == rows[i].Penalty {
			rows[i].Place = rows[i-1].Place
			continue
		}
		rows[i].Place = i + 1
	}
	return rows
}

func compareICPC(a, b Entry) int {
	if a.Solved != b.Solved {
		return b.Solved - a.Solved
	}
	if a.Penalty != b.Penalty {
		return a.Penalty - b.Penalty
	}
	if c := compareTimes(a.LastSolvedAt, b.LastSolvedAt); c != 0 {
		return c
	}
	return bytes.Compare(a.Registration[:], b.Registration[:])
}

// compareFinal orders two correct final answers: the earlier one wins, and two
// in the same instant are decided by the registration id so that the winner
// does not change between two reads.
func compareFinal(a, b Entry) int {
	if c := a.FinalAt.Compare(*b.FinalAt); c != 0 {
		return c
	}
	return bytes.Compare(a.Registration[:], b.Registration[:])
}

func comparePoints(a, b Entry) int {
	if a.Points != b.Points {
		return b.Points - a.Points
	}
	if c := compareTimes(a.LastScoredAt, b.LastScoredAt); c != 0 {
		return c
	}
	return bytes.Compare(a.Registration[:], b.Registration[:])
}

func sameResult(a, b Entry) bool {
	if a.Points != b.Points {
		return false
	}
	if a.LastScoredAt == nil || b.LastScoredAt == nil {
		return a.LastScoredAt == nil && b.LastScoredAt == nil
	}
	return a.LastScoredAt.Equal(*b.LastScoredAt)
}

// compareTimes orders earlier first and a missing moment last.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		return a.Compare(*b)
	}
}
