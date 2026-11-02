package leaderboard

import "github.com/devrdn/db-contest/backend/internal/contests"

// The standing of an exported report. Only a finished contest's report is
// settled; an earlier one may still move.
const (
	ReportPreliminary = "preliminary"
	ReportFinal       = "final"
	ReportArchived    = "archived"
)

// ReportStatus names how settled a report is for a contest in the given
// status: draft, published and running are preliminary.
func ReportStatus(contestStatus string) string {
	switch contestStatus {
	case contests.StatusFinished:
		return ReportFinal
	case contests.StatusArchived:
		return ReportArchived
	default:
		return ReportPreliminary
	}
}

// RankReport orders the rows of an exported report. Places and prizes are
// counted among the non-disqualified entries only, so a place in the report
// is the place on the public table; the disqualified follow all the others in
// their own result order, with no place, no prize and no win.
//
// Ranking after filtering, not before, is what lets winner mode hand the win
// to the earliest final answer that still counts. It never changes entries.
func RankReport(scoring string, places contests.PrizePlaces, entries []Entry, grid Grid) []Row {
	var kept, disqualified []Entry
	for _, e := range entries {
		if e.Disqualified {
			disqualified = append(disqualified, e)
		} else {
			kept = append(kept, e)
		}
	}

	rows := rankEntries(scoring, kept, grid)
	AwardPrizes(rows, scoring, places)

	out := rankEntries(scoring, disqualified, grid)
	for i := range out {
		out[i].Place, out[i].Prize, out[i].Winner = 0, 0, false
	}
	return append(rows, out...)
}

func rankEntries(scoring string, entries []Entry, grid Grid) []Row {
	if scoring == contests.ScoringICPC {
		return RankICPC(entries, grid)
	}
	return Rank(scoring, entries)
}
