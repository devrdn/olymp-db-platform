package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// A transaction's now() is one moment, so every row here states its own time.
var boardStart = time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

func boardAt(minute int) time.Time { return boardStart.Add(time.Duration(minute) * time.Minute) }

type boardFixture struct {
	ctx      context.Context
	contest  uuid.UUID
	question uuid.UUID
	final    uuid.UUID
}

func newBoardFixture(t *testing.T, ctx context.Context) boardFixture {
	t.Helper()
	author := makeUser(t, ctx, "board-author")
	f := boardFixture{ctx: ctx, contest: makeContest(t, ctx, author.ID)}
	questions := NewQuestions(testPool)
	text, err := questions.Create(ctx, contests.Question{ContestID: f.contest, Kind: contests.KindText, IsVisible: true})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	final, err := questions.Create(ctx, contests.Question{ContestID: f.contest, Kind: contests.KindFinal, IsVisible: true})
	if err != nil {
		t.Fatalf("create question: %v", err)
	}
	f.question, f.final = text.ID, final.ID
	return f
}

// participant enrols a new account, registered at the given minute.
func (f boardFixture) participant(t *testing.T, login string, minute int) uuid.UUID {
	t.Helper()
	user := makeUser(t, f.ctx, login)
	id := makeRegistration(t, f.ctx, f.contest, user.ID)
	exec(t, f.ctx, `UPDATE registrations SET created_at = $2 WHERE id = $1`, id, boardAt(minute))
	return id
}

func (f boardFixture) answer(t *testing.T, registration, question uuid.UUID, attempt int, correct bool, points, minute int) {
	t.Helper()
	exec(t, f.ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		VALUES ($1, $2, $3, 'an answer', $4, $5, $6)`,
		registration, question, attempt, correct, points, boardAt(minute))
}

func exec(t *testing.T, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func byLogin(entries []leaderboard.Entry) map[string]leaderboard.Entry {
	out := make(map[string]leaderboard.Entry, len(entries))
	for _, e := range entries {
		out[e.Login] = e
	}
	return out
}

func TestStandingsAggregateEachRegistrationUpToTheCutoff(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newBoardFixture(t, ctx)
		alice := f.participant(t, "board-alice", 0)
		f.answer(t, alice, f.question, 1, false, 0, 5)
		f.answer(t, alice, f.question, 2, true, 8, 10)
		f.answer(t, alice, f.final, 1, true, 20, 30)
		// Exactly at the cutoff: not on the table.
		f.answer(t, alice, f.final, 2, true, 99, 60)

		entries, err := NewLeaderboard(testPool).Standings(ctx, leaderboard.Query{
			ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringPoints, Limit: 10,
		})
		if err != nil {
			t.Fatalf("Standings() = %v", err)
		}
		got := byLogin(entries)["board-alice"]
		if got.Points != 28 || got.Solved != 2 {
			t.Errorf("points, solved = %d, %d, want 28, 2", got.Points, got.Solved)
		}
		if got.LastScoredAt == nil || !got.LastScoredAt.Equal(boardAt(30)) {
			t.Errorf("LastScoredAt = %v, want %v", got.LastScoredAt, boardAt(30))
		}
		if got.FinalAt == nil || !got.FinalAt.Equal(boardAt(30)) {
			t.Errorf("FinalAt = %v, want %v", got.FinalAt, boardAt(30))
		}
	})
}

// A registration made after the cutoff is not on a frozen table, or its row
// would appear with a zero and say that somebody joined during the freeze.
func TestStandingsLeaveOutRegistrationsAfterTheCutoffAndTheDisqualified(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newBoardFixture(t, ctx)
		f.participant(t, "board-early", 0)
		f.participant(t, "board-late", 61)
		banned := f.participant(t, "board-banned", 0)
		exec(t, ctx, `UPDATE registrations SET status = 'disqualified' WHERE id = $1`, banned)

		repo := NewLeaderboard(testPool)
		public, err := repo.Standings(ctx, leaderboard.Query{ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringPoints, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		logins := byLogin(public)
		if _, ok := logins["board-late"]; ok {
			t.Error("a registration after the cutoff is on the table")
		}
		if _, ok := logins["board-banned"]; ok {
			t.Error("a disqualified registration is on the public table")
		}
		if _, ok := logins["board-early"]; !ok {
			t.Error("a registration before the cutoff is missing")
		}

		staff, err := repo.Standings(ctx, leaderboard.Query{ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringPoints, Limit: 10, IncludeDisqualified: true})
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := byLogin(staff)["board-banned"]; !ok || !e.Disqualified {
			t.Errorf("staff table entry = %+v, %v, want the disqualified row marked", e, ok)
		}
	})
}

func TestStandingsMarkADeletedAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newBoardFixture(t, ctx)
		gone := makeUser(t, ctx, "board-gone")
		makeRegistration(t, ctx, f.contest, gone.ID)
		exec(t, ctx, `UPDATE registrations SET created_at = $2 WHERE user_id = $1`, gone.ID, boardAt(0))
		exec(t, ctx, `UPDATE users SET status = 'deleted' WHERE id = $1`, gone.ID)

		entries, err := NewLeaderboard(testPool).Standings(ctx, leaderboard.Query{ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringPoints, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || !entries[0].AccountDeleted {
			t.Errorf("entries = %+v, want the one deleted account marked", entries)
		}
	})
}

// Cutting the list must never cut the top of the table: by points normally,
// and with the winner first in winner mode even when the winner has fewer
// points than everybody else.
func TestStandingsCutTheListBelowTheTopOfTheTable(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newBoardFixture(t, ctx)
		for i, login := range []string{"board-p1", "board-p2", "board-p3"} {
			id := f.participant(t, login, 0)
			f.answer(t, id, f.question, 1, true, 30-i*10, 10)
		}
		winner := f.participant(t, "board-winner", 0)
		f.answer(t, winner, f.final, 1, true, 1, 20)

		repo := NewLeaderboard(testPool)
		points, err := repo.Standings(ctx, leaderboard.Query{ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringPoints, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(points) != 2 || points[0].Login != "board-p1" || points[1].Login != "board-p2" {
			t.Errorf("points order = %+v, want p1, p2", points)
		}

		winnerMode, err := repo.Standings(ctx, leaderboard.Query{ContestID: f.contest, Cutoff: boardAt(60), Scoring: contests.ScoringWinner, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(winnerMode) != 2 || winnerMode[0].Login != "board-winner" || winnerMode[1].Login != "board-p1" {
			t.Errorf("winner order = %+v, want winner, p1", winnerMode)
		}
	})
}

// The reveal is written once, and it does not move updated_at: the reclaim
// sweep measures a finished contest's grace from that column, and pressing
// "reveal" must not quietly keep its databases alive for another day.
func TestMarkRevealedWritesOnceAndLeavesUpdatedAtAlone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newBoardFixture(t, ctx)
		var before time.Time
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT updated_at FROM contests WHERE id = $1`, f.contest).Scan(&before); err != nil {
			t.Fatal(err)
		}

		repo := NewLeaderboard(testPool)
		first, newly, err := repo.MarkRevealed(ctx, f.contest, boardAt(200))
		if err != nil || !newly || !first.Equal(boardAt(200)) {
			t.Fatalf("first MarkRevealed() = %v, %v, %v", first, newly, err)
		}
		second, newly, err := repo.MarkRevealed(ctx, f.contest, boardAt(300))
		if err != nil || newly || !second.Equal(boardAt(200)) {
			t.Fatalf("second MarkRevealed() = %v, %v, %v, want the first moment and not newly", second, newly, err)
		}

		var after time.Time
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT updated_at FROM contests WHERE id = $1`, f.contest).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !after.Equal(before) {
			t.Errorf("updated_at moved from %v to %v", before, after)
		}

		if _, _, err := repo.MarkRevealed(ctx, uuid.New(), boardAt(1)); err != contests.ErrNotFound {
			t.Errorf("MarkRevealed(missing) = %v, want contests.ErrNotFound", err)
		}
	})
}
