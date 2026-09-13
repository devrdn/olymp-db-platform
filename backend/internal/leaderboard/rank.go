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
// The registration id is the last key only so that the order of equal rows
// does not change between two reads; a place never depends on it.
func Rank(scoring string, entries []Entry) []Row {
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
