package leaderboard_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
)

var (
	start = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	end   = start.Add(3 * time.Hour)
)

func contest(status string, freezeMin *int, revealed *time.Time) contests.Contest {
	s, e := start, end
	return contests.Contest{
		Status: status, Scoring: contests.ScoringPoints, StartsAt: &s, EndsAt: &e,
		LeaderboardNames: contests.LeaderboardNamesLogin, LeaderboardFreezeMin: freezeMin,
		LeaderboardRevealedAt: revealed,
	}
}

func minutes(n int) *int { return &n }

// The whole table of states from the design, one row each, including both
// sides of the freeze boundary.
func TestDecideNamesTheStateAndTheCutoff(t *testing.T) {
	freezeAt := end.Add(-30 * time.Minute)
	revealed := end.Add(time.Hour)
	during := start.Add(time.Hour)

	cases := []struct {
		name       string
		contest    contests.Contest
		now        time.Time
		wantState  string
		wantCutoff time.Time
		wantFrozen bool
	}{
		{"published", contest(contests.StatusPublished, nil, nil), start.Add(-time.Hour),
			leaderboard.StateNotStarted, start.Add(-time.Hour), false},
		{"running, no freeze", contest(contests.StatusRunning, nil, nil), during,
			leaderboard.StateLive, during, false},
		{"running, before the freeze", contest(contests.StatusRunning, minutes(30), nil), freezeAt.Add(-time.Second),
			leaderboard.StateLive, freezeAt.Add(-time.Second), false},
		{"running, exactly at the freeze", contest(contests.StatusRunning, minutes(30), nil), freezeAt,
			leaderboard.StateFrozen, freezeAt, true},
		{"finished, frozen and not revealed", contest(contests.StatusFinished, minutes(30), nil), end.Add(2 * time.Hour),
			leaderboard.StateFrozen, freezeAt, true},
		{"archived, frozen and not revealed", contest(contests.StatusArchived, minutes(30), nil), end.Add(48 * time.Hour),
			leaderboard.StateFrozen, freezeAt, true},
		{"finished, revealed", contest(contests.StatusFinished, minutes(30), &revealed), end.Add(2 * time.Hour),
			leaderboard.StateFinal, end.Add(2 * time.Hour), false},
		{"finished, never frozen", contest(contests.StatusFinished, nil, nil), end.Add(time.Hour),
			leaderboard.StateFinal, end.Add(time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := leaderboard.Decide(tc.contest, tc.now)
			if err != nil {
				t.Fatalf("Decide() = %v", err)
			}
			if got.State != tc.wantState || !got.Cutoff.Equal(tc.wantCutoff) {
				t.Errorf("Decide() = %s at %v, want %s at %v", got.State, got.Cutoff, tc.wantState, tc.wantCutoff)
			}
			if (got.FrozenAt != nil) != tc.wantFrozen {
				t.Errorf("FrozenAt = %v, want set: %v", got.FrozenAt, tc.wantFrozen)
			}
		})
	}
}

// A draft has no table for anybody outside its staff, and says so the same way
// a contest that does not exist does.
func TestDecideRefusesADraft(t *testing.T) {
	if _, err := leaderboard.Decide(contest(contests.StatusDraft, nil, nil), start); !errors.Is(err, leaderboard.ErrNotFound) {
		t.Errorf("Decide() = %v, want ErrNotFound", err)
	}
}

// A cell's state is decided by what it holds, in the design's order: a solve
// before the cutoff wins, attempts waiting since the freeze come next, then
// wrong attempts, and a question nobody touched is untried.
func TestCellStateFollowsWhatTheCellHolds(t *testing.T) {
	cases := []struct {
		name string
		cell leaderboard.Cell
		want string
	}{
		{"nothing", leaderboard.Cell{}, leaderboard.CellUntried},
		{"wrong attempts only", leaderboard.Cell{Wrong: 2}, leaderboard.CellFailed},
		{"solved after wrong attempts", leaderboard.Cell{SolvedAt: at(12), Wrong: 2}, leaderboard.CellSolved},
		{"attempts since the freeze", leaderboard.Cell{Pending: 1}, leaderboard.CellPending},
		{"wrong before the freeze, attempts since", leaderboard.Cell{Wrong: 3, Pending: 2}, leaderboard.CellPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cell.State(); got != tc.want {
				t.Errorf("State() = %s, want %s", got, tc.want)
			}
		})
	}
	if got := (leaderboard.Cell{SolvedAt: at(12), Wrong: 2}).SolvedOnAttempt(); got != 3 {
		t.Errorf("SolvedOnAttempt() = %d, want 3", got)
	}
}

// A question on the grid is named by its position among the visible ones,
// never by an identifier, and the names do not run out after Z.
func TestQuestionLetterNamesAQuestionByItsPosition(t *testing.T) {
	cases := map[int]string{0: "A", 1: "B", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"}
	for position, want := range cases {
		if got := leaderboard.QuestionLetter(position); got != want {
			t.Errorf("QuestionLetter(%d) = %q, want %q", position, got, want)
		}
	}
}
