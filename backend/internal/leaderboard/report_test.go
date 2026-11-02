package leaderboard_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

func dq(e leaderboard.Entry) leaderboard.Entry {
	e.Disqualified = true
	return e
}

func logins(rows []leaderboard.Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Login
	}
	return out
}

func places(rows []leaderboard.Row) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Place
	}
	return out
}

func TestAReportPlacesRowsAmongTheNonDisqualifiedOnly(t *testing.T) {
	a, d, b := entry("a", 30, at(5)), dq(entry("d", 25, at(6))), entry("b", 20, at(7))

	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{a, d, b}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"a", "b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 2, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportSharesAPlaceOnATie(t *testing.T) {
	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{
		entry("a", 30, at(5)), entry("b", 30, at(5)), entry("c", 20, at(7)),
	}, leaderboard.Grid{})

	if got, want := places(rows), []int{1, 1, 3}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportLeavesARowWithoutAnAnswerUnplaced(t *testing.T) {
	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{}, []leaderboard.Entry{
		silent(entry("quiet", 0, nil)), entry("a", 30, at(5)),
	}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"a", "quiet"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
}

func TestAReportAwardsPrizesByTheRecountedPlaces(t *testing.T) {
	a, d, b := entry("a", 30, at(5)), dq(entry("d", 25, at(6))), entry("b", 20, at(7))

	rows := leaderboard.RankReport(contests.ScoringPoints, contests.PrizePlaces{First: 1, Second: 1}, []leaderboard.Entry{a, d, b}, leaderboard.Grid{})

	prizes := map[string]int{}
	for _, r := range rows {
		prizes[r.Login] = r.Prize
	}
	if want := map[string]int{"a": 1, "b": 2, "d": 0}; !maps.Equal(prizes, want) {
		t.Errorf("prizes = %v, want %v", prizes, want)
	}
}

func TestAWinnerReportPassesTheWinOverADisqualifiedEarlierFinal(t *testing.T) {
	d, b := dq(entry("d", 5, at(10))), entry("b", 5, at(20))
	d.FinalAt, b.FinalAt = at(10), at(20)

	rows := leaderboard.RankReport(contests.ScoringWinner, contests.PrizePlaces{}, []leaderboard.Entry{d, b}, leaderboard.Grid{})

	if got, want := logins(rows), []string{"b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if rows[0].Place != 1 || !rows[0].Winner || rows[0].Prize != 1 {
		t.Errorf("first row = %+v, want b as the placed winner with the first prize", rows[0])
	}
	if rows[1].Place != 0 || rows[1].Winner || rows[1].Prize != 0 {
		t.Errorf("second row = %+v, want d unplaced, not the winner, without a prize", rows[1])
	}
}

func TestAnICPCReportRanksWithoutTheDisqualified(t *testing.T) {
	first := at(10)
	g := grid(first)
	a := icpcEntry("a", 1, 20, at(15), solvedCell(at(15)))
	b := icpcEntry("b", 1, 40, at(30), solvedCell(at(30)))
	d := dq(icpcEntry("d", 2, 10, first, solvedCell(first)))

	rows := leaderboard.RankReport(contests.ScoringICPC, contests.PrizePlaces{First: 1}, []leaderboard.Entry{d, b, a}, g)

	if got, want := logins(rows), []string{"a", "b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if got, want := places(rows), []int{1, 2, 0}; !slices.Equal(got, want) {
		t.Errorf("places = %v, want %v", got, want)
	}
	if rows[0].Prize != 1 || rows[2].Prize != 0 {
		t.Errorf("prizes = %d, %d, want 1 for a and 0 for d", rows[0].Prize, rows[2].Prize)
	}
	if rows[2].Cells[0].First {
		t.Error("the disqualified row's cell is marked first")
	}
}

func TestAReportDoesNotTouchTheEntriesItIsGiven(t *testing.T) {
	g := grid(at(10))
	a := icpcEntry("a", 1, 20, at(10), solvedCell(at(10)))
	d := dq(icpcEntry("d", 1, 10, at(10), solvedCell(at(10))))
	entries := []leaderboard.Entry{d, a}

	leaderboard.RankReport(contests.ScoringICPC, contests.PrizePlaces{First: 1}, entries, g)

	if entries[0].Login != "d" || entries[1].Login != "a" {
		t.Errorf("order = %v, want it unchanged", logins([]leaderboard.Row{{Entry: entries[0]}, {Entry: entries[1]}}))
	}
	if entries[0].Cells[0].First || entries[1].Cells[0].First {
		t.Error("a first mark was written into the given cells")
	}
}

func TestAReportStatusFollowsTheContest(t *testing.T) {
	for status, want := range map[string]string{
		contests.StatusDraft:     leaderboard.ReportPreliminary,
		contests.StatusPublished: leaderboard.ReportPreliminary,
		contests.StatusRunning:   leaderboard.ReportPreliminary,
		contests.StatusFinished:  leaderboard.ReportFinal,
		contests.StatusArchived:  leaderboard.ReportArchived,
	} {
		if got := leaderboard.ReportStatus(status); got != want {
			t.Errorf("ReportStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
