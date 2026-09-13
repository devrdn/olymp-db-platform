package postgres

import (
	"context"
	"fmt"
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

// icpcFixture is a fixed-window ICPC contest that opened at boardStart with
// three questions in this order: A, a hidden one, and B. The grid is A and B.
type icpcFixture struct {
	boardFixture
	a, hidden, b uuid.UUID
}

func newICPCFixture(t *testing.T, ctx context.Context, penaltyMin int) icpcFixture {
	t.Helper()
	author := makeUser(t, ctx, "icpc-author")
	f := icpcFixture{boardFixture: boardFixture{ctx: ctx, contest: makeContest(t, ctx, author.ID)}}
	exec(t, ctx, `UPDATE contests SET scoring = 'icpc', icpc_penalty_min = $2, starts_at = $3, ends_at = $4 WHERE id = $1`,
		f.contest, penaltyMin, boardAt(0), boardAt(180))
	questions := NewQuestions(testPool)
	for _, q := range []struct {
		id      *uuid.UUID
		visible bool
	}{{&f.a, true}, {&f.hidden, false}, {&f.b, true}} {
		created, err := questions.Create(ctx, contests.Question{ContestID: f.contest, Kind: contests.KindText, IsVisible: q.visible})
		if err != nil {
			t.Fatalf("create question: %v", err)
		}
		*q.id = created.ID
	}
	return f
}

// answerAt stores an ICPC answer, which never carries points, at an exact moment.
func (f icpcFixture) answerAt(t *testing.T, registration, question uuid.UUID, attempt int, correct bool, at time.Time) {
	t.Helper()
	exec(t, f.ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		VALUES ($1, $2, $3, 'an answer', $4, 0, $5)`,
		registration, question, attempt, correct, at)
}

func (f icpcFixture) standings(t *testing.T, q leaderboard.Query) map[string]leaderboard.Entry {
	t.Helper()
	entries, _ := f.table(t, q)
	return byLogin(entries)
}

// table runs the ICPC query and returns the rows in order and the grid.
func (f icpcFixture) table(t *testing.T, q leaderboard.Query) ([]leaderboard.Entry, leaderboard.Grid) {
	t.Helper()
	q.ContestID, q.Scoring = f.contest, contests.ScoringICPC
	if q.Limit == 0 {
		q.Limit = 10
	}
	entries, grid, err := NewLeaderboard(testPool).ICPCStandings(f.ctx, q)
	if err != nil {
		t.Fatalf("ICPCStandings() = %v", err)
	}
	return entries, grid
}

func cellString(c leaderboard.Cell) string {
	solved := "-"
	if c.SolvedAt != nil {
		solved = c.SolvedAt.UTC().Format("15:04:05")
	}
	return fmt.Sprintf("{solved %s minute %d wrong %d pending %d}", solved, c.Minute, c.Wrong, c.Pending)
}

// The solving minute is whole minutes from the start, rounded down: 59 seconds
// is minute 0 and 60 seconds is minute 1.
func TestICPCStandingsRoundTheSolvingMinuteDown(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		early := f.participant(t, "icpc-59s", 0)
		onTheMinute := f.participant(t, "icpc-60s", 0)
		f.answerAt(t, early, f.a, 1, true, boardStart.Add(59*time.Second+999*time.Millisecond))
		f.answerAt(t, onTheMinute, f.a, 1, true, boardStart.Add(60*time.Second))

		got := f.standings(t, leaderboard.Query{Cutoff: boardAt(60)})
		if e := got["icpc-59s"]; len(e.Cells) != 2 || e.Cells[0].Minute != 0 || e.Penalty != 0 || e.Solved != 1 {
			t.Errorf("59 s: %+v, want minute 0, penalty 0, solved 1", e)
		}
		if e := got["icpc-60s"]; len(e.Cells) != 2 || e.Cells[0].Minute != 1 || e.Penalty != 1 {
			t.Errorf("60 s: %+v, want minute 1, penalty 1", e)
		}
	})
}

// With an individual timer the minute is measured from the participant's own
// start, not from the window's.
func TestICPCStandingsMeasureAnIndividualTimerFromTheParticipantsStart(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		exec(t, ctx, `UPDATE contests SET timing = 'individual', duration_min = 60 WHERE id = $1`, f.contest)
		late := f.participant(t, "icpc-late-start", 0)
		exec(t, ctx, `UPDATE registrations SET started_at = $2 WHERE id = $1`, late, boardAt(30))
		f.answerAt(t, late, f.a, 1, true, boardAt(45).Add(30*time.Second))

		e := f.standings(t, leaderboard.Query{Cutoff: boardAt(120)})["icpc-late-start"]
		if len(e.Cells) != 2 || e.Cells[0].Minute != 15 || e.Penalty != 15 {
			t.Errorf("entry = %+v, want minute 15 from started_at", e)
		}
	})
}

// Only wrong attempts before the solve cost, at the contest's own penalty: a
// wrong attempt after the solve and the wrong attempts on an unsolved question
// cost nothing.
func TestICPCStandingsChargeOnlyWrongAttemptsBeforeTheSolve(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 7)
		alice := f.participant(t, "icpc-alice", 0)
		f.answerAt(t, alice, f.a, 1, false, boardAt(1))
		f.answerAt(t, alice, f.a, 2, false, boardAt(2))
		f.answerAt(t, alice, f.a, 3, true, boardAt(10))
		f.answerAt(t, alice, f.a, 4, false, boardAt(11))
		f.answerAt(t, alice, f.b, 1, false, boardAt(5))
		f.answerAt(t, alice, f.b, 2, false, boardAt(6))

		e := f.standings(t, leaderboard.Query{Cutoff: boardAt(60)})["icpc-alice"]
		if e.Solved != 1 || e.Penalty != 10+7*2 {
			t.Errorf("solved, penalty = %d, %d, want 1, %d", e.Solved, e.Penalty, 10+7*2)
		}
		if e.LastSolvedAt == nil || !e.LastSolvedAt.Equal(boardAt(10)) {
			t.Errorf("LastSolvedAt = %v, want %v", e.LastSolvedAt, boardAt(10))
		}
		if len(e.Cells) != 2 {
			t.Fatalf("cells = %d, want 2", len(e.Cells))
		}
		a, b := e.Cells[0], e.Cells[1]
		if a.SolvedAt == nil || !a.SolvedAt.Equal(boardAt(10)) || a.Minute != 10 || a.Wrong != 2 || a.Pending != 0 {
			t.Errorf("cell A = %s, want solved at minute 10 after 2 wrong", cellString(a))
		}
		if b.SolvedAt != nil || b.Wrong != 2 || b.Minute != 0 {
			t.Errorf("cell B = %s, want unsolved with 2 wrong", cellString(b))
		}
	})
}

// A hidden question is neither on the grid nor in the count, even solved.
func TestICPCStandingsLeaveAHiddenQuestionOffTheGrid(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		alice := f.participant(t, "icpc-hidden", 0)
		f.answerAt(t, alice, f.hidden, 1, true, boardAt(3))
		f.answerAt(t, alice, f.b, 1, true, boardAt(4))

		entries, grid := f.table(t, leaderboard.Query{Cutoff: boardAt(60)})
		e := byLogin(entries)["icpc-hidden"]
		if e.Solved != 1 || e.Penalty != 4 || len(e.Cells) != 2 || e.Cells[0].SolvedAt != nil || e.Cells[1].Minute != 4 {
			t.Errorf("entry = %+v, want only B solved, at minute 4, on a grid of A and B", e)
		}
		if grid.Questions != 2 || len(grid.FirstSolves) != 2 || grid.FirstSolves[0] != nil ||
			grid.FirstSolves[1] == nil || !grid.FirstSolves[1].Equal(boardAt(4)) {
			t.Errorf("grid = %d questions, first solves %v; want 2, [nil, minute 4]", grid.Questions, grid.FirstSolves)
		}
	})
}

// Pending attempts are counted only when the query asks, over [From, Until),
// and only on a question not solved before the cutoff; nothing after the
// cutoff moves solved, penalty or a cell's solve — not even a correct answer.
func TestICPCStandingsCountPendingAttemptsInTheWindowOnUnsolvedQuestions(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		bob := f.participant(t, "icpc-bob", 0)
		f.answerAt(t, bob, f.a, 1, true, boardAt(30))
		f.answerAt(t, bob, f.a, 2, false, boardAt(70)) // after its solve: never pending
		f.answerAt(t, bob, f.b, 1, false, boardAt(40))
		f.answerAt(t, bob, f.b, 2, false, boardAt(60)) // exactly at the freeze: pending
		f.answerAt(t, bob, f.b, 3, true, boardAt(65))  // correct, still only a count
		f.answerAt(t, bob, f.b, 4, false, boardAt(90)) // exactly at Until: not yet

		cutoff := boardAt(60)
		frozen := f.standings(t, leaderboard.Query{
			Cutoff: cutoff, Pending: &leaderboard.Window{From: cutoff, Until: boardAt(90)},
		})["icpc-bob"]
		if frozen.Solved != 1 || frozen.Penalty != 30 || frozen.LastSolvedAt == nil || !frozen.LastSolvedAt.Equal(boardAt(30)) {
			t.Errorf("frozen entry = %+v, want solved 1, penalty 30, last solve at minute 30", frozen)
		}
		if len(frozen.Cells) != 2 {
			t.Fatalf("cells = %d, want 2", len(frozen.Cells))
		}
		if a := frozen.Cells[0]; a.Pending != 0 || a.SolvedAt == nil {
			t.Errorf("cell A = %s, want solved without pending", cellString(a))
		}
		if b := frozen.Cells[1]; b.SolvedAt != nil || b.Minute != 0 || b.Wrong != 1 || b.Pending != 2 {
			t.Errorf("cell B = %s, want unsolved, 1 wrong before the freeze, 2 pending", cellString(b))
		}

		unasked := f.standings(t, leaderboard.Query{Cutoff: cutoff})["icpc-bob"]
		for _, c := range unasked.Cells {
			if c.Pending != 0 {
				t.Errorf("cell %s carries pending attempts nobody asked for", cellString(c))
			}
		}
	})
}

// A registration made after the cutoff is not on a frozen ICPC table, even
// with attempts inside the pending window: its row would say that somebody
// joined during the freeze, and its pending count what they did since.
func TestICPCStandingsLeaveOutARegistrationAfterTheCutoffWithPendingAttempts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		f.participant(t, "icpc-before", 0)
		late := f.participant(t, "icpc-joined-late", 70)
		f.answerAt(t, late, f.a, 1, false, boardAt(75))

		cutoff := boardAt(60)
		got := f.standings(t, leaderboard.Query{
			Cutoff: cutoff, Pending: &leaderboard.Window{From: cutoff, Until: boardAt(90)},
		})
		if _, ok := got["icpc-joined-late"]; ok {
			t.Errorf("a registration after the cutoff is on the frozen table: %+v", got["icpc-joined-late"])
		}
		if _, ok := got["icpc-before"]; !ok {
			t.Error("a registration before the cutoff is missing")
		}
	})
}

// The order and the cut match the ranking: more solved first, then less
// penalty, then the earlier last solve — so LIMIT never cuts the top.
func TestICPCStandingsCutTheListBelowTheTopOfTheTable(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		none := f.participant(t, "icpc-none", 0)
		f.answerAt(t, none, f.a, 1, false, boardAt(1))
		slow := f.participant(t, "icpc-slow", 0)
		f.answerAt(t, slow, f.a, 1, true, boardAt(50))
		fast := f.participant(t, "icpc-fast", 0)
		f.answerAt(t, fast, f.a, 1, true, boardAt(40))
		two := f.participant(t, "icpc-two", 0)
		f.answerAt(t, two, f.a, 1, true, boardAt(55))
		f.answerAt(t, two, f.b, 1, true, boardAt(59))
		banned := f.participant(t, "icpc-banned", 0)
		f.answerAt(t, banned, f.a, 1, true, boardAt(1))
		f.answerAt(t, banned, f.b, 1, true, boardAt(2))
		exec(t, ctx, `UPDATE registrations SET status = 'disqualified' WHERE id = $1`, banned)

		entries, _ := f.table(t, leaderboard.Query{Cutoff: boardAt(60), Limit: 2})
		if len(entries) != 2 || entries[0].Login != "icpc-two" || entries[1].Login != "icpc-fast" {
			t.Errorf("order = %+v, want icpc-two, icpc-fast", entries)
		}

		staff := f.standings(t, leaderboard.Query{Cutoff: boardAt(60), IncludeDisqualified: true})
		if e, ok := staff["icpc-banned"]; !ok || !e.Disqualified || e.Solved != 2 {
			t.Errorf("staff entry = %+v, %v, want the disqualified row with 2 solved", e, ok)
		}
	})
}

// A table with nobody on it still knows its grid: the letters and the cells
// of the first row to arrive must agree, and there is no row to count from.
func TestICPCStandingsReturnTheGridWithNoRegistrations(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)

		entries, grid := f.table(t, leaderboard.Query{Cutoff: boardAt(60)})
		if len(entries) != 0 || grid.Questions != 2 || len(grid.FirstSolves) != 2 ||
			grid.FirstSolves[0] != nil || grid.FirstSolves[1] != nil {
			t.Errorf("entries = %+v, grid = %+v; want no rows on a grid of 2 unsolved questions", entries, grid)
		}
	})
}

// A question's earliest solve is the contest's, not the page's: the first
// solver is below the row bound here, a disqualified registration solved
// earlier still, the staff query lists that registration, and an answer
// after the cutoff is earlier than nothing. Two solves in the same instant
// both carry the earliest moment.
func TestICPCStandingsNameEachQuestionsEarliestSolveOverTheWholeContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newICPCFixture(t, ctx, 20)
		banned := f.participant(t, "icpc-banned", 0)
		f.answerAt(t, banned, f.a, 1, true, boardAt(1))
		f.answerAt(t, banned, f.b, 1, true, boardAt(2))
		exec(t, ctx, `UPDATE registrations SET status = 'disqualified' WHERE id = $1`, banned)
		top := f.participant(t, "icpc-top", 0)
		f.answerAt(t, top, f.a, 1, true, boardAt(20))
		f.answerAt(t, top, f.b, 1, true, boardAt(30))
		tie := f.participant(t, "icpc-tie", 0)
		f.answerAt(t, tie, f.a, 1, true, boardAt(25))
		f.answerAt(t, tie, f.b, 1, true, boardAt(30))
		early := f.participant(t, "icpc-early-one", 0)
		f.answerAt(t, early, f.a, 1, true, boardAt(5))
		after := f.participant(t, "icpc-after-cutoff", 0)
		f.answerAt(t, after, f.b, 1, true, boardAt(61))

		for _, staff := range []bool{false, true} {
			entries, grid := f.table(t, leaderboard.Query{Cutoff: boardAt(60), Limit: 2, IncludeDisqualified: staff})
			if _, ok := byLogin(entries)["icpc-early-one"]; ok {
				t.Fatalf("staff %v: the first solver of A is on a page of 2; the test no longer cuts it off", staff)
			}
			if len(grid.FirstSolves) != 2 || grid.FirstSolves[0] == nil || grid.FirstSolves[1] == nil ||
				!grid.FirstSolves[0].Equal(boardAt(5)) || !grid.FirstSolves[1].Equal(boardAt(30)) {
				t.Errorf("staff %v: first solves = %v, want minute 5 and minute 30", staff, grid.FirstSolves)
			}
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
