package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// watchFixture is one contest with its participants, and helpers that write
// their history at chosen times: inside a test transaction now() is one
// instant, so every row names its own time.
type watchFixture struct {
	t        *testing.T
	ctx      context.Context
	contest  uuid.UUID
	question uuid.UUID
	base     time.Time
}

func newWatchFixture(t *testing.T, ctx context.Context) *watchFixture {
	t.Helper()
	owner := makeUser(t, ctx, "watch-owner-"+uuid.NewString()[:8])
	f := &watchFixture{t: t, ctx: ctx, contest: makeContest(t, ctx, owner.ID),
		base: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
	f.question = f.makeQuestion(1)
	return f
}

func (f *watchFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := storage.QuerierFrom(f.ctx, testPool).Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", strings.Fields(sql)[0:3], err)
	}
}

func (f *watchFixture) makeQuestion(ord int) uuid.UUID {
	f.t.Helper()
	var id uuid.UUID
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx,
		`INSERT INTO questions (contest_id, ord, kind, points) VALUES ($1, $2, 'text', 10) RETURNING id`,
		f.contest, ord).Scan(&id); err != nil {
		f.t.Fatalf("create question: %v", err)
	}
	return id
}

// participant enrols a new account and returns the registration and the
// account.
func (f *watchFixture) participant(login string) (uuid.UUID, uuid.UUID) {
	f.t.Helper()
	user := makeUser(f.t, f.ctx, login+"-"+uuid.NewString()[:8])
	return makeRegistration(f.t, f.ctx, f.contest, user.ID), user.ID
}

// at is the fixture's base time plus d.
func (f *watchFixture) at(d time.Duration) time.Time { return f.base.Add(d) }

// query journals one finished query at a time and returns its id.
func (f *watchFixture) query(reg uuid.UUID, sql, status, ip string, at time.Time) int64 {
	f.t.Helper()
	var address any
	if ip != "" {
		address = ip
	}
	var id int64
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx, `
		INSERT INTO query_log (registration_id, request_id, sql_text, status, ip, sql_fingerprint,
		                       duration_ms, row_count, executed_at, completed_at, error_text)
		VALUES ($1, gen_random_uuid(), $2, $3, $4::inet, $5, 7, 3, $6, $6,
		        CASE WHEN $3 IN ('error', 'rejected') THEN 'boom' END)
		RETURNING id`,
		reg, sql, status, address, monitor.Fingerprint(sql), at).Scan(&id); err != nil {
		f.t.Fatalf("journal a query: %v", err)
	}
	return id
}

// answer records an attempt at a time.
func (f *watchFixture) answer(reg, question uuid.UUID, attempt int, correct bool, at time.Time) uuid.UUID {
	f.t.Helper()
	points := 0
	if correct {
		points = 10
	}
	var id uuid.UUID
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx, `
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, points_awarded, submitted_at)
		VALUES ($1, $2, $3::int, 'v' || $3::int, $4, $5, $6) RETURNING id`,
		reg, question, attempt, correct, points, at).Scan(&id); err != nil {
		f.t.Fatalf("record an answer: %v", err)
	}
	return id
}

// event stores one participant event at a time.
func (f *watchFixture) event(reg uuid.UUID, payload monitor.Payload, at time.Time) int64 {
	f.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	var id int64
	if err := storage.QuerierFrom(f.ctx, testPool).QueryRow(f.ctx, `
		INSERT INTO participant_events (contest_id, registration_id, kind, payload, created_at)
		VALUES ($1, $2, $3, $4::jsonb, $5) RETURNING id`,
		f.contest, reg, string(payload.Kind()), string(body), at).Scan(&id); err != nil {
		f.t.Fatalf("store an event: %v", err)
	}
	return id
}

// rosterRow finds one registration's row.
func rosterRow(t *testing.T, roster monitor.Roster, reg uuid.UUID) monitor.RosterRow {
	t.Helper()
	for _, row := range roster.Rows {
		if row.Registration == reg {
			return row
		}
	}
	t.Fatalf("registration %s is not on the roster", reg)
	return monitor.RosterRow{}
}

func TestWatchRosterCountsWhatEachParticipantDid(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		busy, _ := f.participant("busy")
		idle, _ := f.participant("idle")

		f.query(busy, "select 1", "ok", "192.0.2.1", f.at(time.Minute))
		f.query(busy, "select nope", "error", "192.0.2.1", f.at(2*time.Minute))
		f.query(busy, "select slow", "timeout", "192.0.2.1", f.at(3*time.Minute))
		f.query(busy, "drop table x", "rejected", "192.0.2.1", f.at(4*time.Minute))
		f.answer(busy, f.question, 1, false, f.at(5*time.Minute))
		f.query(busy, "select 2", "ok", "192.0.2.1", f.at(5*time.Minute+30*time.Second))
		f.answer(busy, f.question, 2, true, f.at(6*time.Minute))
		f.event(busy, monitor.PageLeft{AwayMs: 1500}, f.at(7*time.Minute))
		f.event(busy, monitor.PageLeft{AwayMs: 2500}, f.at(8*time.Minute))
		f.event(busy, monitor.Paste{Target: monitor.PasteNotes, Chars: 5, Text: "hello"}, f.at(9*time.Minute))

		roster, err := NewWatch(testPool).Roster(ctx, f.contest, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(roster.Rows) != 2 || roster.Truncated {
			t.Fatalf("rows = %d, truncated = %v, want both participants", len(roster.Rows), roster.Truncated)
		}
		got := rosterRow(t, roster, busy)
		if got.Queries != 5 || got.QueryErrors != 2 || got.QueryRejected != 1 || got.Addresses != 1 {
			t.Errorf("queries = %d/%d errors/%d rejected/%d addresses, want 5/2/1/1",
				got.Queries, got.QueryErrors, got.QueryRejected, got.Addresses)
		}
		if got.Correct != 1 || got.Wrong != 1 {
			t.Errorf("answers = %d correct, %d wrong, want 1 and 1", got.Correct, got.Wrong)
		}
		if got.PageLeft != 2 || got.AwayMs != 4000 || got.Pastes != 1 {
			t.Errorf("absences = %d for %d ms, pastes = %d, want 2, 4000, 1", got.PageLeft, got.AwayMs, got.Pastes)
		}
		if got.LastActivity == nil || !got.LastActivity.Equal(f.at(9*time.Minute)) {
			t.Errorf("last activity = %v, want the paste's time", got.LastActivity)
		}
		if got.Flags().Any() {
			t.Errorf("flags = %+v, want none", got.Flags())
		}

		quiet := rosterRow(t, roster, idle)
		if quiet.Queries != 0 || quiet.LastActivity != nil {
			t.Errorf("idle participant: %d queries, last activity %v, want nothing", quiet.Queries, quiet.LastActivity)
		}
	})
}

func TestWatchRosterIsBoundedAndSaysSo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		for _, login := range []string{"a", "b", "c"} {
			f.participant(login)
		}
		roster, err := NewWatch(testPool).Roster(ctx, f.contest, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(roster.Rows) != 2 || !roster.Truncated {
			t.Errorf("rows = %d, truncated = %v, want 2 and truncated", len(roster.Rows), roster.Truncated)
		}
	})
}

// Each flag is raised exactly past its threshold (design §5): one participant
// sits on the threshold and does not raise it, the next crosses it.
func TestWatchRosterRaisesEachFlagAtItsThreshold(t *testing.T) {
	longSQL := "select name, alibi from suspects where city = 'Chisinau' order by name"
	if len(longSQL) < monitor.IdenticalQueryMinChars {
		t.Fatal("the fixture query must be long enough to compare")
	}
	justShort := strings.Repeat("x", monitor.IdenticalQueryMinChars-1)
	exactly := strings.Repeat("y", monitor.IdenticalQueryMinChars)

	cases := []struct {
		name  string
		flag  func(monitor.Flags) bool
		below func(f *watchFixture, reg, other uuid.UUID)
		above func(f *watchFixture, reg, other uuid.UUID)
	}{
		{
			name: "multiple addresses",
			flag: func(fl monitor.Flags) bool { return fl.MultipleIPs },
			below: func(f *watchFixture, reg, _ uuid.UUID) {
				f.query(reg, "select 1", "ok", "192.0.2.1", f.at(time.Minute))
				f.query(reg, "select 2", "ok", "192.0.2.1", f.at(2*time.Minute))
			},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				f.query(reg, "select 1", "ok", "192.0.2.1", f.at(time.Minute))
				f.query(reg, "select 2", "ok", "192.0.2.2", f.at(2*time.Minute))
			},
		},
		{
			name:  "an address change event",
			flag:  func(fl monitor.Flags) bool { return fl.MultipleIPs },
			below: func(*watchFixture, uuid.UUID, uuid.UUID) {},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.IPChanged{From: mustAddr("192.0.2.1"), To: mustAddr("192.0.2.9")}, f.at(time.Minute))
			},
		},
		{
			name:  "parallel sessions",
			flag:  func(fl monitor.Flags) bool { return fl.ParallelSessions },
			below: func(*watchFixture, uuid.UUID, uuid.UUID) {},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.ParallelSession{OtherIP: mustAddr("192.0.2.5"), UserAgent: "x"}, f.at(time.Minute))
			},
		},
		{
			name: "total absence",
			flag: func(fl monitor.Flags) bool { return fl.LongAbsence },
			below: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.PageLeft{AwayMs: monitor.LongAbsenceTotal.Milliseconds()}, f.at(time.Minute))
			},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.PageLeft{AwayMs: monitor.LongAbsenceTotal.Milliseconds() + 1}, f.at(time.Minute))
			},
		},
		{
			name: "number of absences",
			flag: func(fl monitor.Flags) bool { return fl.LongAbsence },
			below: func(f *watchFixture, reg, _ uuid.UUID) {
				for i := range monitor.LongAbsenceCount {
					f.event(reg, monitor.PageLeft{AwayMs: 1000}, f.at(time.Duration(i)*time.Minute))
				}
			},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				for i := range monitor.LongAbsenceCount + 1 {
					f.event(reg, monitor.PageLeft{AwayMs: 1000}, f.at(time.Duration(i)*time.Minute))
				}
			},
		},
		{
			name: "a correct answer with no successful query before it",
			flag: func(fl monitor.Flags) bool { return fl.AnswerWithoutQueries },
			below: func(f *watchFixture, reg, _ uuid.UUID) {
				f.answer(reg, f.question, 1, false, f.at(time.Minute))
				f.query(reg, "select 1", "ok", "", f.at(2*time.Minute))
				f.answer(reg, f.question, 2, true, f.at(3*time.Minute))
			},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				// A successful query before the previous attempt and a failed
				// one since: neither led to this answer.
				f.query(reg, "select 1", "ok", "", f.at(0))
				f.answer(reg, f.question, 1, false, f.at(time.Minute))
				f.query(reg, "select nope", "error", "", f.at(2*time.Minute))
				f.answer(reg, f.question, 2, true, f.at(3*time.Minute))
			},
		},
		{
			name: "a large paste into the editor",
			flag: func(fl monitor.Flags) bool { return fl.LargePaste },
			below: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.Paste{Target: monitor.PasteEditor, Chars: monitor.LargePasteChars}, f.at(time.Minute))
				f.event(reg, monitor.Paste{Target: monitor.PasteNotes, Chars: 5000}, f.at(time.Minute))
			},
			above: func(f *watchFixture, reg, _ uuid.UUID) {
				f.event(reg, monitor.Paste{Target: monitor.PasteAnswer, Chars: monitor.LargePasteChars + 1}, f.at(time.Minute))
			},
		},
		{
			name: "identical queries",
			flag: func(fl monitor.Flags) bool { return fl.IdenticalQueries },
			below: func(f *watchFixture, reg, other uuid.UUID) {
				// Too short, failed on one side, or only this participant's.
				f.query(reg, justShort, "ok", "", f.at(time.Minute))
				f.query(other, justShort, "ok", "", f.at(time.Minute))
				f.query(reg, longSQL, "ok", "", f.at(2*time.Minute))
				f.query(other, longSQL, "error", "", f.at(2*time.Minute))
				f.query(reg, exactly+" padded   ", "ok", "", f.at(3*time.Minute))
			},
			above: func(f *watchFixture, reg, other uuid.UUID) {
				f.query(reg, exactly, "ok", "", f.at(time.Minute))
				f.query(other, "  "+strings.ToUpper(exactly)+"\n", "ok", "", f.at(2*time.Minute))
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				f := newWatchFixture(t, ctx)
				below, _ := f.participant("below")
				above, _ := f.participant("above")
				otherBelow, _ := f.participant("other-below")
				otherAbove, _ := f.participant("other-above")
				c.below(f, below, otherBelow)
				c.above(f, above, otherAbove)

				roster, err := NewWatch(testPool).Roster(ctx, f.contest, 10)
				if err != nil {
					t.Fatal(err)
				}
				if c.flag(rosterRow(t, roster, below).Flags()) {
					t.Errorf("on the threshold: flag raised, %+v", rosterRow(t, roster, below))
				}
				if !c.flag(rosterRow(t, roster, above).Flags()) {
					t.Errorf("past the threshold: flag not raised, %+v", rosterRow(t, roster, above))
				}
			})
		})
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// journals are the tables no organiser's read may scan whole.
var journals = map[string]bool{"query_log": true, "participant_events": true, "submissions": true, "audit_log": true}

// explainingQuerier EXPLAINs every statement before running it, and keeps
// each plan's sequential scans of a journal.
type explainingQuerier struct {
	storage.Querier
	t     *testing.T
	scans *[]string
}

func (q explainingQuerier) explain(ctx context.Context, sql string, args ...any) {
	q.t.Helper()
	var raw []byte
	if err := q.Querier.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
		q.t.Fatalf("EXPLAIN %s: %v", sql, err)
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		q.t.Fatalf("read the plan: %v", err)
	}
	var walk func(n planNode)
	walk = func(n planNode) {
		if n.NodeType == "Seq Scan" && journals[n.Relation] {
			*q.scans = append(*q.scans, n.Relation+" in:\n"+sql+"\nplan: "+string(raw))
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
}

type planNode struct {
	NodeType string     `json:"Node Type"`
	Relation string     `json:"Relation Name"`
	Plans    []planNode `json:"Plans"`
}

func (q explainingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.explain(ctx, sql, args...)
	return q.Querier.Query(ctx, sql, args...)
}

func (q explainingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.explain(ctx, sql, args...)
	return q.Querier.QueryRow(ctx, sql, args...)
}

// TestWatchReadsScanNoJournal runs every organiser's read against a database
// holding a representative olympiad — three contests of forty participants,
// each with a few hundred queries, events, answers and sign-ins, beside
// everything else the test database holds — and EXPLAINs each statement
// exactly as the read sends it: none may plan a sequential scan of
// query_log, participant_events, submissions or audit_log (design §9).
//
// The planner is left free, not forced off sequential scans: with the
// statistics ANALYZE gathers on this data, a plan that falls back to a scan
// is the plan production would run.
func TestWatchReadsScanNoJournal(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		var contests []*watchFixture
		for range 3 {
			contests = append(contests, newWatchFixture(t, ctx))
		}
		// The rest of a year: other contests' participants, ten times as
		// many, so the contest being read is the small part of each journal
		// it is in production.
		history := newWatchFixture(t, ctx)
		loadOlympiad(t, history, 99, 400)
		for ci, f := range contests {
			loadOlympiad(t, f, ci, 40)
		}
		for _, table := range []string{"users", "registrations", "query_log", "participant_events", "submissions", "audit_log", "workspace_revisions"} {
			if _, err := q.Exec(ctx, "ANALYZE "+table); err != nil {
				t.Fatalf("analyze %s: %v", table, err)
			}
		}

		f := contests[1]
		var reg uuid.UUID
		if err := q.QueryRow(ctx, `SELECT id FROM registrations WHERE contest_id = $1 LIMIT 1`, f.contest).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		var scans []string
		watch := NewWatch(testPool)
		watch.wrap = func(inner storage.Querier) storage.Querier {
			return explainingQuerier{Querier: inner, t: t, scans: &scans}
		}
		middle := monitor.Cursor{At: f.at(20 * time.Minute), Source: monitor.SourceQuery, ID: "1"}
		reads := map[string]func() error{
			"roster": func() error { _, err := watch.Roster(ctx, f.contest, monitor.MaxRosterRows); return err },
			"contest feed, newest": func() error {
				_, err := watch.Feed(ctx, monitor.FeedQuery{Contest: f.contest, Limit: monitor.MaxFeedPage})
				return err
			},
			"contest feed, after": func() error {
				_, err := watch.Feed(ctx, monitor.FeedQuery{Contest: f.contest, After: &middle, Limit: monitor.MaxFeedPage})
				return err
			},
			"contest feed, before, filtered": func() error {
				_, err := watch.Feed(ctx, monitor.FeedQuery{Contest: f.contest, Before: &middle,
					Kinds: []string{monitor.FeedKindQuery, string(monitor.KindPaste), monitor.FeedSignInFailed},
					From:  f.at(0), Until: f.at(time.Hour), Limit: 50})
				return err
			},
			"timeline": func() error {
				_, err := watch.Feed(ctx, monitor.FeedQuery{Contest: f.contest, Registration: reg, After: &middle, Limit: monitor.MaxFeedPage})
				return err
			},
			"participant": func() error { _, err := watch.Participant(ctx, f.contest, reg); return err },
			"queries": func() error {
				_, err := watch.Queries(ctx, monitor.QueriesQuery{Contest: f.contest, Registration: reg,
					Status: "error", Search: "suspects", Before: &middle})
				return err
			},
			"answers":   func() error { _, err := watch.Answers(ctx, f.contest, reg, monitor.MaxAttemptQueries); return err },
			"workspace": func() error { _, err := watch.Workspace(ctx, reg); return err },
			"revision": func() error {
				_, err := watch.Revision(ctx, reg, 1)
				if errors.Is(err, monitor.ErrRevisionNotFound) {
					return nil
				}
				return err
			},
		}
		for name, read := range reads {
			scans = scans[:0]
			if err := read(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, scan := range scans {
				t.Errorf("%s scans a journal whole: %s", name, scan)
			}
		}
	})
}

// loadOlympiad enrols participants in f's contest and gives each three
// hundred queries, a hundred events, thirty answers, fifty sign-ins, ten
// failed ones and twenty revisions — written in time order across the
// participants, as a real olympiad interleaves them on disk.
func loadOlympiad(t *testing.T, f *watchFixture, tag, participants int) {
	t.Helper()
	f.exec(`
		WITH people AS (
		    INSERT INTO users (login, full_name, status, password_hash)
		    SELECT 'load-' || $2::int || '-' || n || '-' || substr(md5(random()::text), 1, 8), 'Load ' || n, 'active', 'x'
		    FROM generate_series(1, $4::int) n
		    RETURNING id
		)
		INSERT INTO registrations (contest_id, user_id, started_at, created_at)
		SELECT $1, id, $3::timestamptz, $3::timestamptz - interval '1 hour' FROM people`, f.contest, tag, f.base, participants)
	f.exec(`
		INSERT INTO query_log (registration_id, request_id, sql_text, status, ip, sql_fingerprint, executed_at)
		SELECT r.id, gen_random_uuid(),
		       'select * from suspects where id = ' || n || ' and name like ''%' || md5(n::text) || '%''',
		       (ARRAY['ok','ok','ok','error','rejected'])[1 + n % 5], '192.0.2.1', n % 50,
		       $2::timestamptz + n * interval '10 seconds'
		FROM registrations r CROSS JOIN generate_series(1, 300) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base)
	f.exec(`
		INSERT INTO participant_events (contest_id, registration_id, kind, payload, created_at)
		SELECT $1, r.id, CASE WHEN n % 2 = 0 THEN 'page_left' ELSE 'paste' END,
		       CASE WHEN n % 2 = 0 THEN '{"away_ms": 4000}'::jsonb
		            ELSE '{"target": "editor", "chars": 300, "text": "x"}'::jsonb END,
		       $2::timestamptz + n * interval '30 seconds'
		FROM registrations r CROSS JOIN generate_series(1, 100) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base)
	f.exec(`
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, submitted_at)
		SELECT r.id, $2, n, 'v', n = 30, $3::timestamptz + n * interval '5 minutes'
		FROM registrations r CROSS JOIN generate_series(1, 30) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.question, f.base)
	f.exec(`
		INSERT INTO audit_log (actor_id, action, entity, entity_id, ip, created_at)
		SELECT r.user_id, 'auth.login', 'user', r.user_id::text, '192.0.2.1', $2::timestamptz + n * interval '20 minutes'
		FROM registrations r CROSS JOIN generate_series(1, 50) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base)
	f.exec(`
		INSERT INTO audit_log (action, entity, payload, created_at)
		SELECT 'auth.login_failed', 'user', jsonb_build_object('login', u.login, 'reason', 'wrong_password'),
		       $2::timestamptz + n * interval '20 minutes'
		FROM registrations r JOIN users u ON u.id = r.user_id CROSS JOIN generate_series(1, 10) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base)
	f.exec(`
		INSERT INTO workspace_revisions (registration_id, document, body, started_at, updated_at)
		SELECT r.id, 'notes', repeat('n', 200), $2::timestamptz + n * interval '1 minute', $2::timestamptz + n * interval '1 minute'
		FROM registrations r CROSS JOIN generate_series(1, 20) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base)
}
