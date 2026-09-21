package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// profileFixture is one account with contests of its own and, beside every
// one of them, another account's registration in the same contest: what the
// reads must never pick up is present in the data, not merely absent from it.
type profileFixture struct {
	t        *testing.T
	ctx      context.Context
	user     uuid.UUID
	stranger uuid.UUID
	base     time.Time
}

func newProfileFixture(t *testing.T, ctx context.Context) *profileFixture {
	t.Helper()
	return &profileFixture{t: t, ctx: ctx,
		user:     makeUser(t, ctx, "profile-"+uuid.NewString()[:8]).ID,
		stranger: makeUser(t, ctx, "stranger-"+uuid.NewString()[:8]).ID,
		base:     time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
}

func (f *profileFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := storage.QuerierFrom(f.ctx, testPool).Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

// contest stores a contest of the given status, starting at base+offset, and
// enrols both accounts in it. It returns the contest and the caller's own
// registration.
func (f *profileFixture) contest(status string, offset time.Duration) (uuid.UUID, uuid.UUID) {
	f.t.Helper()
	contest := makeContest(f.t, f.ctx, f.user)
	starts := f.base.Add(offset)
	f.exec(`UPDATE contests SET status = $2, starts_at = $3, ends_at = $4, timing = 'fixed' WHERE id = $1`,
		contest, status, starts, starts.Add(2*time.Hour))
	f.exec(`INSERT INTO contest_languages (contest_id, lang, is_default) VALUES ($1, 'en', true)`, contest)
	f.exec(`INSERT INTO contest_translations (contest_id, lang, title) VALUES ($1, 'en', 'The Library Murder')`, contest)
	reg := makeRegistration(f.t, f.ctx, contest, f.user)
	makeRegistration(f.t, f.ctx, contest, f.stranger)
	return contest, reg
}

func (f *profileFixture) question(contest uuid.UUID, ord int) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx, `
		INSERT INTO questions (contest_id, ord, kind, points, is_visible)
		VALUES ($1, $2, 'text', 10, true) RETURNING id`, contest, ord).Scan(&id); err != nil {
		f.t.Fatalf("create question: %v", err)
	}
	return id
}

func (f *profileFixture) query(reg uuid.UUID, status string, at time.Time) {
	f.t.Helper()
	f.exec(`
		INSERT INTO query_log (registration_id, request_id, sql_text, status, sql_fingerprint, executed_at)
		VALUES ($1, gen_random_uuid(), 'select 1', $2, 0, $3)`, reg, status, at)
}

func (f *profileFixture) answer(reg, question uuid.UUID, attempt int, correct bool, at time.Time) {
	f.t.Helper()
	points := 0
	if correct {
		points = 10
	}
	f.exec(`
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		VALUES ($1, $2, $3::int, 'v' || $3::int, $4, $5, $6)`, reg, question, attempt, correct, points, at)
}

func TestProfileSummaryCountsOnlyTheCallersOwnWork(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		finished, mine := f.contest(contests.StatusFinished, -48*time.Hour)
		_, running := f.contest(contests.StatusRunning, 0)
		question := f.question(finished, 1)
		second := f.question(finished, 2)

		f.query(mine, "ok", f.base)
		f.query(mine, "error", f.base.Add(time.Minute))
		f.query(running, "ok", f.base.Add(2*time.Minute))
		// Two correct attempts at one question count as one solved question.
		f.answer(mine, question, 1, false, f.base)
		f.answer(mine, question, 2, true, f.base.Add(time.Minute))
		f.answer(mine, second, 1, true, f.base.Add(2*time.Minute))
		// The stranger's own work, in the same contests.
		strangerReg := f.strangerIn(finished)
		f.query(strangerReg, "ok", f.base)
		f.answer(strangerReg, question, 1, true, f.base)
		f.exec(`UPDATE registrations SET status = 'finished' WHERE id = $1`, mine)

		got, err := NewProfile(testPool).Summary(ctx, f.user)
		if err != nil {
			t.Fatalf("Summary() = %v", err)
		}
		if got.Contests != 2 || got.Finished != 1 || got.Queries != 3 || got.Solved != 2 {
			t.Errorf("Summary() = %+v, want 2 contests, 1 finished, 3 queries, 2 solved", got)
		}
	})
}

// strangerIn finds the other account's registration in a contest, so a test
// can put work under it.
func (f *profileFixture) strangerIn(contest uuid.UUID) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx,
		`SELECT id FROM registrations WHERE contest_id = $1 AND user_id = $2`, contest, f.stranger).Scan(&id); err != nil {
		f.t.Fatalf("find the stranger's registration: %v", err)
	}
	return id
}

func TestProfileEnrolmentsReadTheCallersOwnContestsNewestFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		older, olderReg := f.contest(contests.StatusFinished, -72*time.Hour)
		newer, newerReg := f.contest(contests.StatusFinished, -24*time.Hour)
		// A contest the caller is not on at all.
		outside := makeContest(t, ctx, f.stranger)
		makeRegistration(t, ctx, outside, f.stranger)

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("Enrolments() returned %d rows, want the caller's two", len(rows))
		}
		if rows[0].Contest.ID != newer || rows[1].Contest.ID != older {
			t.Errorf("order = %s, %s, want the newer contest first", rows[0].Contest.ID, rows[1].Contest.ID)
		}
		if rows[0].Participant.ID != newerReg || rows[1].Participant.ID != olderReg {
			t.Errorf("the rows carry %s and %s, want the caller's own registrations",
				rows[0].Participant.ID, rows[1].Participant.ID)
		}
		first := rows[0]
		if first.Contest.Status != contests.StatusFinished || first.Contest.EndsAt == nil ||
			first.Contest.Translations["en"].Title != "The Library Murder" ||
			len(first.Contest.Languages) != 1 || first.Contest.Timing != contests.TimingFixed {
			t.Errorf("the contest arrives as %+v, want it whole", first.Contest)
		}
		if first.Participant.UserID != f.user || first.Participant.Login == "" {
			t.Errorf("the registration arrives as %+v", first.Participant)
		}
	})
}

// The page is picked before anything is counted, so a limit cuts the rows
// the newest-first order puts at the top and the result columns are computed
// for those rows only.
func TestProfileEnrolmentsStopAtTheLimitAtTheTopOfTheOrder(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		var newest []uuid.UUID
		for i := range 5 {
			// The first is the newest; each one after it started an hour
			// earlier.
			contest, _ := f.contest(contests.StatusFinished, -time.Duration(i+1)*time.Hour)
			if i < 2 {
				newest = append(newest, contest)
			}
		}

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 2)
		if err != nil || len(rows) != 2 {
			t.Fatalf("Enrolments(limit 2) returned %d rows, %v", len(rows), err)
		}
		if rows[0].Contest.ID != newest[0] || rows[1].Contest.ID != newest[1] {
			t.Errorf("the page is %s, %s; want the two newest %s, %s",
				rows[0].Contest.ID, rows[1].Contest.ID, newest[0], newest[1])
		}
	})
}

// The list carries the participant's own numbers, counted from their own
// submissions and nobody else's, and agreeing with the table the contest is
// judged by: the same points and the same solved count postgres.Leaderboard
// computes for the same registration. Two statements compute these numbers
// and this is what keeps them one answer.
func TestProfileEnrolmentsCarryTheOwnResultTheLeaderboardAgreesWith(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		contest, mine := f.contest(contests.StatusFinished, -24*time.Hour)
		first, second, untried := f.question(contest, 1), f.question(contest, 2), f.question(contest, 3)
		stranger := f.strangerIn(contest)

		f.answer(mine, first, 1, false, f.base)
		f.answer(mine, first, 2, true, f.base.Add(10*time.Minute))
		f.answer(mine, second, 1, true, f.base.Add(20*time.Minute))
		_ = untried
		// The stranger scores more, in the same contest, on the same
		// questions.
		f.answer(stranger, first, 1, true, f.base)
		f.answer(stranger, second, 1, true, f.base)

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		got := rows[0].Result
		if got.Scoring != contests.ScoringPoints || got.Points != 20 || got.Solved != 2 {
			t.Fatalf("result = %+v, want 20 points and 2 solved", got)
		}

		entries, err := NewLeaderboard(testPool).Standings(ctx, leaderboard.Query{
			// Past the registrations too: a row made after the cutoff is on no
			// table, and these were made a moment ago.
			ContestID: contest, Cutoff: time.Now().Add(time.Hour), Scoring: contests.ScoringPoints, Limit: 10,
		})
		if err != nil {
			t.Fatalf("Standings() = %v", err)
		}
		for _, entry := range entries {
			if entry.Registration != mine {
				continue
			}
			if entry.Points != got.Points || entry.Solved != got.Solved {
				t.Errorf("the table says %d points and %d solved, the list says %d and %d",
					entry.Points, entry.Solved, got.Points, got.Solved)
			}
			return
		}
		t.Fatal("the caller is not on the table at all")
	})
}

// A draft contest is on no profile, in the list or in the four numbers.
//
// A roster may be filled while a contest is still being written, so a
// registration in a draft exists long before anybody is meant to know the
// contest does. The catalogue already refuses to show one (Contests.List: a
// draft is nobody's business but its authors'), and the profile refuses for
// the same reason — otherwise a member of the roster would read its title,
// its status and its schedule here before it is published. Both reads exclude
// it, so the header's numbers count exactly the rows the list shows.
func TestProfileReadsLeaveADraftContestOut(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		published, _ := f.contest(contests.StatusPublished, 24*time.Hour)
		// Newer than the published one, so an unfiltered read would put it
		// first rather than merely include it.
		draft, draftReg := f.contest(contests.StatusDraft, 48*time.Hour)
		question := f.question(draft, 1)
		f.query(draftReg, "ok", f.base)
		f.answer(draftReg, question, 1, true, f.base)

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		if len(rows) != 1 || rows[0].Contest.ID != published {
			t.Fatalf("Enrolments() returned %d rows, want only the published contest %s", len(rows), published)
		}

		summary, err := NewProfile(testPool).Summary(ctx, f.user)
		if err != nil {
			t.Fatalf("Summary() = %v", err)
		}
		if summary.Contests != 1 || summary.Queries != 0 || summary.Solved != 0 {
			t.Errorf("Summary() = %+v, want 1 contest and nothing of the draft's own work", summary)
		}
	})
}

// Archived contests stay: a profile is a history view, and archiving is how a
// finished olympiad is put away, not how it is taken from the people who sat
// it.
func TestProfileReadsKeepAnArchivedContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		archived, _ := f.contest(contests.StatusArchived, -72*time.Hour)

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		if len(rows) != 1 || rows[0].Contest.ID != archived {
			t.Fatalf("Enrolments() returned %d rows, want the archived contest %s", len(rows), archived)
		}
		summary, err := NewProfile(testPool).Summary(ctx, f.user)
		if err != nil {
			t.Fatalf("Summary() = %v", err)
		}
		if summary.Contests != 1 {
			t.Errorf("Summary() = %+v, want the archived contest counted", summary)
		}
	})
}

// The ICPC row is the one with a formula worth pinning: solved counts the
// visible questions solved, and the penalty is each solve's minute from the
// start plus the contest's penalty for every wrong attempt before it. Both
// are checked against postgres.Leaderboard's own ICPC computation on the
// same data.
func TestProfileEnrolmentsCarryTheICPCResultTheLeaderboardAgreesWith(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		contest, mine := f.contest(contests.StatusFinished, 0)
		f.exec(`UPDATE contests SET scoring = 'icpc', icpc_penalty_min = 20 WHERE id = $1`, contest)
		first, second := f.question(contest, 1), f.question(contest, 2)
		hidden := f.question(contest, 3)
		f.exec(`UPDATE questions SET is_visible = false WHERE id = $1`, hidden)
		stranger := f.strangerIn(contest)

		// One wrong attempt then a solve 30 minutes in: 30 + 20 = 50.
		f.answer(mine, first, 1, false, f.base.Add(5*time.Minute))
		f.answer(mine, first, 2, true, f.base.Add(30*time.Minute))
		// A clean solve 10 minutes in: 10.
		f.answer(mine, second, 1, true, f.base.Add(10*time.Minute))
		// A hidden question is on no ICPC grid, so it costs and counts
		// nothing.
		f.answer(mine, hidden, 1, true, f.base.Add(90*time.Minute))
		f.answer(stranger, first, 1, true, f.base)

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		got := rows[0].Result
		if got.Scoring != contests.ScoringICPC || got.Solved != 2 || got.Penalty != 60 {
			t.Fatalf("result = %+v, want 2 solved and 60 penalty minutes", got)
		}
		// An ICPC contest has no points: Submit writes points_awarded = 0 in
		// that mode, and the table reports none, so the list must not add up
		// whatever a contest switched out of icpc left behind.
		if got.Points != 0 {
			t.Errorf("result = %+v, want no points in icpc scoring", got)
		}

		entries, _, err := NewLeaderboard(testPool).ICPCStandings(ctx, leaderboard.Query{
			ContestID: contest, Cutoff: time.Now().Add(time.Hour), Scoring: contests.ScoringICPC, Limit: 10,
		})
		if err != nil {
			t.Fatalf("ICPCStandings() = %v", err)
		}
		for _, entry := range entries {
			if entry.Registration != mine {
				continue
			}
			if entry.Solved != got.Solved || entry.Penalty != got.Penalty || entry.Points != got.Points {
				t.Errorf("the table says %d solved, %d penalty and %d points; the list says %d, %d and %d",
					entry.Solved, entry.Penalty, entry.Points, got.Solved, got.Penalty, got.Points)
			}
			return
		}
		t.Fatal("the caller is not on the table at all")
	})
}

// Under an individual timer a solve's minute is counted from the
// participant's own start, not the contest's — the branch of the penalty
// arithmetic most likely to drift between the two statements that carry it.
func TestProfileEnrolmentsCarryTheICPCResultUnderAnIndividualTimer(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		contest, mine := f.contest(contests.StatusFinished, 0)
		f.exec(`UPDATE contests SET scoring = 'icpc', icpc_penalty_min = 20,
		        timing = 'individual', duration_min = 120 WHERE id = $1`, contest)
		// The participant began half an hour after the contest opened, so a
		// solve at +50 minutes is minute 20 of their own hour, not 50.
		f.exec(`UPDATE registrations SET started_at = $2, status = 'active' WHERE id = $1`,
			mine, f.base.Add(30*time.Minute))
		question := f.question(contest, 1)
		f.answer(mine, question, 1, false, f.base.Add(40*time.Minute))
		f.answer(mine, question, 2, true, f.base.Add(50*time.Minute))

		rows, err := NewProfile(testPool).Enrolments(ctx, f.user, 10)
		if err != nil {
			t.Fatalf("Enrolments() = %v", err)
		}
		got := rows[0].Result
		if got.Solved != 1 || got.Penalty != 40 {
			t.Fatalf("result = %+v, want 1 solved and 20 + 20 penalty minutes", got)
		}

		entries, _, err := NewLeaderboard(testPool).ICPCStandings(ctx, leaderboard.Query{
			ContestID: contest, Cutoff: time.Now().Add(time.Hour), Scoring: contests.ScoringICPC, Limit: 10,
		})
		if err != nil {
			t.Fatalf("ICPCStandings() = %v", err)
		}
		for _, entry := range entries {
			if entry.Registration != mine {
				continue
			}
			if entry.Solved != got.Solved || entry.Penalty != got.Penalty {
				t.Errorf("the table says %d solved and %d penalty, the list says %d and %d",
					entry.Solved, entry.Penalty, got.Solved, got.Penalty)
			}
			return
		}
		t.Fatal("the caller is not on the table at all")
	})
}

func TestProfileActivityCountsOneRegistrationsOwnJournal(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		contest, mine := f.contest(contests.StatusFinished, -24*time.Hour)
		question := f.question(contest, 1)
		stranger := f.strangerIn(contest)

		for _, status := range []string{"ok", "ok", "error", "rejected", "timeout"} {
			f.query(mine, status, f.base)
		}
		f.query(stranger, "ok", f.base)
		f.answer(mine, question, 1, false, f.base.Add(10*time.Minute))
		f.answer(mine, question, 2, true, f.base.Add(30*time.Minute))
		f.answer(stranger, question, 1, true, f.base.Add(time.Hour))

		got, err := NewProfile(testPool).Activity(ctx, mine)
		if err != nil {
			t.Fatalf("Activity() = %v", err)
		}
		if got.Queries != 5 || got.Successful != 2 {
			t.Errorf("Activity() = %+v, want 5 queries of which 2 succeeded", got)
		}
		if got.LastAnswerAt == nil || !got.LastAnswerAt.Equal(f.base.Add(30*time.Minute)) {
			t.Errorf("last answer = %v, want the caller's own last one", got.LastAnswerAt)
		}
	})
}

func TestProfileActivityOfARegistrationThatDidNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newProfileFixture(t, ctx)
		_, mine := f.contest(contests.StatusFinished, -24*time.Hour)

		got, err := NewProfile(testPool).Activity(ctx, mine)
		if err != nil {
			t.Fatalf("Activity() = %v", err)
		}
		if got.Queries != 0 || got.Successful != 0 || got.LastAnswerAt != nil {
			t.Errorf("Activity() = %+v, want an empty session", got)
		}
	})
}

// Every read a profile makes runs against a database holding a
// representative year — four hundred participants and their journals beside
// the caller's own handful of rows — and none of them may scan a journal:
// query_log and submissions are the largest tables there are, and a profile
// is a page anybody signed in may open (the same guarantee
// TestWatchReadsScanNoJournal makes of the organiser's reads).
func TestProfileReadsScanNoJournal(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		f := newProfileFixture(t, ctx)
		_, mine := f.contest(contests.StatusFinished, -24*time.Hour)
		// More registrations than one page carries, so the plan has to pick
		// the page before it counts anything: the result columns are an
		// aggregate per row, and paying for all of them to return fifty is
		// what the page subquery exists to prevent.
		for i := range 60 {
			f.contest(contests.StatusFinished, -time.Duration(i+2)*time.Hour)
		}
		history := newWatchFixture(t, ctx)
		loadOlympiad(t, history, 71, 400)
		for _, table := range []string{"users", "registrations", "contests", "query_log", "submissions"} {
			if _, err := q.Exec(ctx, "ANALYZE "+table); err != nil {
				t.Fatalf("analyze %s: %v", table, err)
			}
		}

		var scans []string
		repo := NewProfile(testPool)
		repo.wrap = func(inner storage.Querier) storage.Querier {
			return explainingQuerier{Querier: inner, t: t, scans: &scans}
		}
		reads := map[string]func() error{
			"summary":    func() error { _, err := repo.Summary(ctx, f.user); return err },
			"enrolments": func() error { _, err := repo.Enrolments(ctx, f.user, 51); return err },
			"activity":   func() error { _, err := repo.Activity(ctx, mine); return err },
		}
		for name, read := range reads {
			scans = nil
			if err := read(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, scan := range scans {
				t.Errorf("%s: %s", name, scan)
			}
		}
	})
}
