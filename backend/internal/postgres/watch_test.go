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
		        CASE $3 WHEN 'error' THEN 'ERROR: boom (SQLSTATE 42703)' WHEN 'rejected' THEN 'boom' END)
		RETURNING id`,
		reg, sql, status, address, monitor.ComparableFingerprint(sql), at).Scan(&id); err != nil {
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
		// Three identical pastes in a row, folded into one event, count as three.
		f.event(busy, monitor.Paste{Target: monitor.PasteNotes, Chars: 2, Text: "hi", Count: 3}, f.at(8*time.Minute+30*time.Second))
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
		if got.PageLeft != 2 || got.AwayMs != 4000 || got.Pastes != 4 {
			t.Errorf("absences = %d for %d ms, pastes = %d, want 2, 4000, 4", got.PageLeft, got.AwayMs, got.Pastes)
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

// TestWatchRosterCostsWhatItShows holds the participants table to the size of
// the table.
//
// The screen recomputes it every monitor.RosterCacheTTL for as long as an
// organiser is looking, so what matters is not that one computation is a range
// but that the range does not lengthen as the contest goes on. The same
// participants are measured twice, the second time with several times the
// history behind them: what the read touches must not have moved, and no
// journal may be touched at all.
//
// The one table the read touches that is derived from a journal is
// contest_query_fingerprints, which holds a row per registration per distinct
// statement rather than per query. The five extra rounds below run the same
// statements again, so they add a quarter of a million journal rows and not
// one row there — which is the property that makes it safe to read.
func TestWatchRosterCostsWhatItShows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		loadOlympiadQueries(t, f, 1, 40, 50)
		analyzeForPlans(t, ctx)

		roster := func(w *Watch) error {
			_, err := w.Roster(ctx, f.contest, monitor.MaxRosterRows)
			return err
		}
		early, earlyAggregate := measureRows(t, roster), measureAggregate(t, ctx, f.contest)

		// The rest of the contest: the same forty participants, five more
		// rounds of everything they do.
		for round := 2; round <= 6; round++ {
			moreHistory(t, f, round, 50)
		}
		analyzeForPlans(t, ctx)
		late, lateAggregate := measureRows(t, roster), measureAggregate(t, ctx, f.contest)
		t.Logf("the table read %d rows and then %d; the aggregate it replaces, %d and then %d",
			total(early), total(late), total(earlyAggregate), total(lateAggregate))

		for table := range journals {
			if early[table] != 0 || late[table] != 0 {
				t.Errorf("the participants table read %d rows of %s and then %d: it reads no journal",
					early[table], table, late[table])
			}
		}
		if total(late) > total(early) {
			t.Errorf("the participants table read %d rows over 40 participants and %d after five more rounds "+
				"of the same contest (%v then %v): its cost follows the history, not the table",
				total(early), total(late), early, late)
		}
		const set = "contest_query_fingerprints"
		if late[set] != early[set] {
			t.Errorf("the set of statements read %d rows and then %d: repeating a statement has to cost it nothing, "+
				"or it is the journal by another name", early[set], late[set])
		}
		// The aggregate is measured on the same data for the same reason the
		// oracle above computes it: to say what was wrong with it. It read the
		// history, so it read more of it as the contest went on.
		if total(lateAggregate) <= total(earlyAggregate) {
			t.Errorf("the aggregate this replaces read %d rows and then %d: the fixture does not grow enough "+
				"to show what it cost", total(earlyAggregate), total(lateAggregate))
		}
	})
}

// measureAggregate runs rosterAggregateSQL under EXPLAIN (ANALYZE) and
// returns how many rows of each relation it really touched.
func measureAggregate(t *testing.T, ctx context.Context, contest uuid.UUID) map[string]int64 {
	t.Helper()
	touched := map[string]int64{}
	measuringQuerier{Querier: storage.QuerierFrom(ctx, testPool), t: t, rows: touched}.
		measure(ctx, rosterAggregateSQL, contest, monitor.MaxRosterRows, monitor.LargePasteChars)
	return touched
}

// total is every row a read touched, whatever it touched.
func total(rows map[string]int64) int64 {
	var n int64
	for _, touched := range rows {
		n += touched
	}
	return n
}

// rosterAggregateSQL is the participants table as it was computed before
// migration 000037: three LATERAL aggregates per registration over the whole
// of query_log, submissions and participant_events, and the fingerprints more
// than one of them ran.
//
// It is kept here, and only here, as the oracle the counters the journals now
// keep are checked against: the numbers on the organiser's screen, and so the
// flags raised on it, must be the same numbers on the same data.
const rosterAggregateSQL = `
WITH regs AS (
    SELECT r.id, u.login
    FROM registrations r
    JOIN users u ON u.id = r.user_id
    WHERE r.contest_id = $1
    ORDER BY u.login, r.id
    LIMIT $2
), counted AS (
    SELECT regs.*,
           ql.total, ql.errors, ql.rejected, ql.addresses, ql.fingerprints,
           sb.correct, sb.wrong, sb.blind,
           ev.page_left, ev.away_ms, ev.pastes, ev.large_pastes, ev.ip_changes, ev.parallel,
           greatest(ql.last_at, sb.last_at, ev.last_at) AS last_at
    FROM regs
    CROSS JOIN LATERAL (
        SELECT count(*) AS total,
               count(*) FILTER (WHERE status IN ('error', 'timeout')) AS errors,
               count(*) FILTER (WHERE status = 'rejected') AS rejected,
               count(DISTINCT ip) AS addresses,
               COALESCE(array_agg(DISTINCT sql_fingerprint) FILTER (
                   WHERE status = 'ok' AND sql_fingerprint IS NOT NULL),
                   '{}') AS fingerprints,
               max(executed_at) AS last_at
        FROM query_log
        WHERE registration_id = regs.id
    ) ql
    CROSS JOIN LATERAL (
        SELECT count(*) FILTER (WHERE a.is_correct) AS correct,
               count(*) FILTER (WHERE NOT a.is_correct) AS wrong,
               count(*) FILTER (WHERE a.is_correct AND NOT EXISTS (
                   SELECT 1 FROM query_log q
                   WHERE q.registration_id = regs.id
                     AND q.status = 'ok'
                     AND q.executed_at < a.submitted_at
                     AND q.executed_at >= COALESCE(a.previous, '-infinity'))) AS blind,
               max(a.submitted_at) AS last_at
        FROM (
            SELECT s.is_correct, s.submitted_at,
                   lag(s.submitted_at) OVER (ORDER BY s.submitted_at, s.id) AS previous
            FROM submissions s
            WHERE s.registration_id = regs.id
        ) a
    ) sb
    CROSS JOIN LATERAL (
        SELECT count(*) FILTER (WHERE kind = 'page_left') AS page_left,
               COALESCE(sum((payload->>'away_ms')::bigint) FILTER (WHERE kind = 'page_left'), 0) AS away_ms,
               COALESCE(sum(COALESCE((payload->>'count')::bigint, 1)) FILTER (WHERE kind = 'paste'), 0) AS pastes,
               count(*) FILTER (WHERE kind = 'paste'
                                  AND payload->>'target' IN ('editor', 'answer')
                                  AND (payload->>'chars')::bigint > $3) AS large_pastes,
               count(*) FILTER (WHERE kind = 'ip_changed') AS ip_changes,
               count(*) FILTER (WHERE kind = 'parallel_session') AS parallel,
               max(created_at) AS last_at
        FROM participant_events
        WHERE registration_id = regs.id
    ) ev
), shared AS (
    SELECT fingerprint
    FROM counted CROSS JOIN LATERAL unnest(counted.fingerprints) AS fingerprint
    GROUP BY fingerprint
    HAVING count(*) > 1
)
SELECT id,
       total, errors, rejected, addresses,
       correct, wrong, blind,
       page_left, away_ms, pastes, large_pastes, ip_changes, parallel,
       (SELECT count(*) FROM unnest(counted.fingerprints) AS own
        WHERE own IN (SELECT fingerprint FROM shared)),
       last_at
FROM counted
ORDER BY login, id`

// aggregatedRow is one row of rosterAggregateSQL.
type aggregatedRow struct {
	registration                   uuid.UUID
	queries, errors, rejected      int
	addresses                      int
	correct, wrong, blind          int
	pageLeft                       int
	awayMs                         int64
	pastes, largePastes            int
	ipChanges, parallel, identical int
	lastActivity                   *time.Time
}

// aggregateRoster computes the participants table the way it was computed
// before the journals kept their own counters.
func aggregateRoster(t *testing.T, ctx context.Context, contest uuid.UUID, limit int) map[uuid.UUID]aggregatedRow {
	t.Helper()
	rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx, rosterAggregateSQL,
		contest, limit, monitor.LargePasteChars)
	if err != nil {
		t.Fatalf("the aggregate the counters replace: %v", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]aggregatedRow{}
	for rows.Next() {
		var r aggregatedRow
		if err := rows.Scan(&r.registration, &r.queries, &r.errors, &r.rejected, &r.addresses,
			&r.correct, &r.wrong, &r.blind, &r.pageLeft, &r.awayMs, &r.pastes, &r.largePastes,
			&r.ipChanges, &r.parallel, &r.identical, &r.lastActivity); err != nil {
			t.Fatalf("scan an aggregated row: %v", err)
		}
		out[r.registration] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("the aggregate the counters replace: %v", err)
	}
	return out
}

// TestWatchRosterAgreesWithTheAggregateItReplaces is the contract: the table
// the journals now count is the table the read used to compute, column for
// column, on data holding every counter and every flag — shared fingerprints,
// a second address, an answer with nothing behind it, a paste over the
// threshold and one under it, and a query journalled before the answer it led
// to and completed after it.
func TestWatchRosterAgreesWithTheAggregateItReplaces(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		f := newWatchFixture(t, ctx)
		// A contest going about its business: shared fingerprints across
		// forty participants, errors, rejections, absences and pastes.
		loadOlympiadQueries(t, f, 1, 40, 60)

		// And the corners that load does not reach.
		long := "select name, alibi from suspects where city = 'Chisinau' order by name"
		roaming, _ := f.participant("roaming")
		f.query(roaming, long, "ok", "192.0.2.1", f.at(time.Minute))
		f.query(roaming, long, "ok", "2001:db8::1", f.at(2*time.Minute))
		f.event(roaming, monitor.IPChanged{From: mustAddr("192.0.2.1"), To: mustAddr("2001:db8::1")}, f.at(3*time.Minute))
		f.event(roaming, monitor.ParallelSession{OtherIP: mustAddr("192.0.2.5"), UserAgent: "x"}, f.at(4*time.Minute))

		guessing, _ := f.participant("guessing")
		f.answer(guessing, f.question, 1, false, f.at(time.Minute))
		f.answer(guessing, f.makeQuestion(2), 1, true, f.at(2*time.Minute))
		f.event(guessing, monitor.Paste{Target: monitor.PasteAnswer, Chars: monitor.LargePasteChars + 1}, f.at(3*time.Minute))
		f.event(guessing, monitor.Paste{Target: monitor.PasteNotes, Chars: 9000}, f.at(4*time.Minute))

		// The two-phase write: the row is journalled before the query runs and
		// completed after the answer was given. Read at any moment after it
		// completes, that query is what stands behind the answer.
		patient, _ := f.participant("patient")
		id := f.query(patient, long, "running", "192.0.2.7", f.at(time.Minute))
		f.answer(patient, f.question, 1, true, f.at(time.Minute+time.Second))
		f.exec(`UPDATE query_log SET status = 'ok', completed_at = $2 WHERE id = $1`, id, f.at(2*time.Minute))

		// Enrolled and idle: no summary row exists for them at all, and the
		// table still has to show them with every counter at nought.
		f.participant("idle")

		roster, err := NewWatch(testPool).Roster(ctx, f.contest, monitor.MaxRosterRows)
		if err != nil {
			t.Fatal(err)
		}
		want := aggregateRoster(t, ctx, f.contest, monitor.MaxRosterRows)
		if len(roster.Rows) != len(want) || len(want) < 43 {
			t.Fatalf("the table has %d rows and the aggregate %d", len(roster.Rows), len(want))
		}

		var sawFlag monitor.Flags
		for _, got := range roster.Rows {
			w, ok := want[got.Registration]
			if !ok {
				t.Fatalf("%s is on the table and not in the aggregate", got.Login)
			}
			if got.Queries != w.queries || got.QueryErrors != w.errors || got.QueryRejected != w.rejected ||
				got.Addresses != w.addresses {
				t.Errorf("%s: queries %d/%d/%d from %d addresses, want %d/%d/%d from %d",
					got.Login, got.Queries, got.QueryErrors, got.QueryRejected, got.Addresses,
					w.queries, w.errors, w.rejected, w.addresses)
			}
			if got.Correct != w.correct || got.Wrong != w.wrong || got.BlindCorrect != w.blind {
				t.Errorf("%s: answers %d correct, %d wrong, %d of them blind, want %d/%d/%d",
					got.Login, got.Correct, got.Wrong, got.BlindCorrect, w.correct, w.wrong, w.blind)
			}
			if got.PageLeft != w.pageLeft || got.AwayMs != w.awayMs || got.Pastes != w.pastes ||
				got.IPChanges != w.ipChanges || got.ParallelSessions != w.parallel ||
				got.IdenticalQueries != w.identical {
				t.Errorf("%s: %d absences for %dms, %d pastes, %d address changes, %d parallel sessions, "+
					"%d shared queries; want %d/%d/%d/%d/%d/%d",
					got.Login, got.PageLeft, got.AwayMs, got.Pastes, got.IPChanges, got.ParallelSessions,
					got.IdenticalQueries, w.pageLeft, w.awayMs, w.pastes, w.ipChanges, w.parallel, w.identical)
			}
			// The largest paste replaced a count of those past the threshold,
			// so the two meet at the flag rather than at the number.
			if got.Flags().LargePaste != (w.largePastes > 0) {
				t.Errorf("%s: large-paste flag %v on a largest paste of %d, and %d pastes past the threshold",
					got.Login, got.Flags().LargePaste, got.LargestPasteChars, w.largePastes)
			}
			if !sameTime(got.LastActivity, w.lastActivity) {
				t.Errorf("%s: last activity %v, want %v", got.Login, got.LastActivity, w.lastActivity)
			}
			sawFlag = or(sawFlag, got.Flags())
		}
		// The data above has to raise every flag, or the agreement is only
		// about the counters nobody looks at.
		if sawFlag != (monitor.Flags{MultipleIPs: true, ParallelSessions: true, LongAbsence: true,
			AnswerWithoutQueries: true, LargePaste: true, IdenticalQueries: true}) {
			t.Errorf("the fixture raises %+v; every flag has to be exercised for this to prove anything", sawFlag)
		}
	})
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func or(a, b monitor.Flags) monitor.Flags {
	return monitor.Flags{
		MultipleIPs:          a.MultipleIPs || b.MultipleIPs,
		ParallelSessions:     a.ParallelSessions || b.ParallelSessions,
		LongAbsence:          a.LongAbsence || b.LongAbsence,
		AnswerWithoutQueries: a.AnswerWithoutQueries || b.AnswerWithoutQueries,
		LargePaste:           a.LargePaste || b.LargePaste,
		IdenticalQueries:     a.IdenticalQueries || b.IdenticalQueries,
	}
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

// derived are tables built from a journal and read the same way it is: a range
// of an index, never a scan. contest_query_fingerprints holds a row per
// registration per distinct statement across every contest the installation
// has ever run, so a read of it not confined to one contest is the same
// mistake as a scan of the journal behind it.
var derived = map[string]bool{"contest_query_fingerprints": true}

// explainingQuerier EXPLAINs every statement before running it, and keeps
// each plan's sequential scans of a journal.
type explainingQuerier struct {
	storage.Querier
	t     *testing.T
	scans *[]string
	// ordered, when it points at true, also refuses a sort over the query
	// log: a keyset page must come out of its index in order, or every page
	// sorts the rest of the registration's range again.
	ordered *bool
}

// keysetJournals are the journals whose keyset pages must be ordered index
// scans. The query log is the one a participant fills by the thousand; the
// answers of one registration are bounded by questions times attempts, a
// page or two, which a bitmap scan and a sort read whole at no cost that
// grows (and the export test counts their rows all the same).
var keysetJournals = map[string]bool{"query_log": true}

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
		// A journal — and anything derived from one — is read only through an
		// index, and only as a range of it: an index scan without a condition
		// walks the whole index.
		if (journals[n.Relation] || derived[n.Relation]) && !rangeReads[n.NodeType] {
			*q.scans = append(*q.scans, n.NodeType+" of "+n.Relation+" in:\n"+sql+"\nplan: "+string(raw))
		}
		// A time bound on the query log belongs in the index condition: left
		// as a filter, the scan reads the registration's whole history on
		// that side and throws most of it away.
		if (n.Relation == "query_log" || strings.HasPrefix(n.Index, "query_log_")) && strings.Contains(n.Filter, "executed_at") {
			*q.scans = append(*q.scans, n.NodeType+" filters query_log by time in:\n"+sql+"\nplan: "+string(raw))
		}
		if journalIndex(n.Index) && n.IndexCond == "" {
			*q.scans = append(*q.scans, n.NodeType+" of "+n.Index+" without a condition in:\n"+sql+"\nplan: "+string(raw))
		}
		if q.ordered != nil && *q.ordered && (n.NodeType == "Sort" || n.NodeType == "Incremental Sort") {
			for _, child := range n.Plans {
				if keysetJournals[child.Relation] {
					*q.scans = append(*q.scans, n.NodeType+" over "+child.Relation+" in:\n"+sql+"\nplan: "+string(raw))
				}
			}
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
}

// rangeReads are the plan nodes that read a table through an index.
var rangeReads = map[string]bool{"Index Scan": true, "Index Only Scan": true, "Bitmap Heap Scan": true}

// journalIndex reports whether an index belongs to a journal or to a table
// derived from one.
func journalIndex(name string) bool {
	for _, tables := range []map[string]bool{journals, derived} {
		for table := range tables {
			if strings.HasPrefix(name, table+"_") {
				return true
			}
		}
	}
	return false
}

type planNode struct {
	NodeType  string     `json:"Node Type"`
	Relation  string     `json:"Relation Name"`
	Index     string     `json:"Index Name"`
	IndexCond string     `json:"Index Cond"`
	Filter    string     `json:"Filter"`
	Plans     []planNode `json:"Plans"`
}

func (q explainingQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.explain(ctx, sql, args...)
	return q.Querier.Query(ctx, sql, args...)
}

func (q explainingQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.explain(ctx, sql, args...)
	return q.Querier.QueryRow(ctx, sql, args...)
}

// SendBatch explains every statement of the batch, which would otherwise
// reach the database without passing through Query at all.
func (q explainingQuerier) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, queued := range b.QueuedQueries {
		q.explain(ctx, queued.SQL, queued.Arguments...)
	}
	return q.Querier.SendBatch(ctx, b)
}

// measuringQuerier runs every statement under EXPLAIN (ANALYZE) first and
// adds up how many rows it really touched, per relation.
//
// A plan's shape says the read is a range; only the count says how long the
// range is. A LATERAL that takes a page per registration is an index range in
// every node and still reads three hundred pages to return one, which is
// exactly what a plan-shape test cannot see.
type measuringQuerier struct {
	storage.Querier
	t *testing.T
	// rows is shared with the test: table name to rows touched.
	rows map[string]int64
}

// measuredNode is a plan node as EXPLAIN ANALYZE reports it. Rows touched is
// what the node handed up plus what its own filter threw away, times the
// number of times it ran: an inner side of a LATERAL runs once per outer row.
type measuredNode struct {
	Relation string         `json:"Relation Name"`
	Rows     float64        `json:"Actual Rows"`
	Loops    float64        `json:"Actual Loops"`
	Removed  float64        `json:"Rows Removed by Filter"`
	Plans    []measuredNode `json:"Plans"`
}

func (q measuringQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.measure(ctx, sql, args...)
	return q.Querier.Query(ctx, sql, args...)
}

func (q measuringQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	q.measure(ctx, sql, args...)
	return q.Querier.QueryRow(ctx, sql, args...)
}

// SendBatch measures every statement of the batch, for the reason
// explainingQuerier.SendBatch gives.
func (q measuringQuerier) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, queued := range b.QueuedQueries {
		q.measure(ctx, queued.SQL, queued.Arguments...)
	}
	return q.Querier.SendBatch(ctx, b)
}

func (q measuringQuerier) measure(ctx context.Context, sql string, args ...any) {
	q.t.Helper()
	var raw []byte
	if err := q.Querier.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
		q.t.Fatalf("EXPLAIN ANALYZE %s: %v", sql, err)
	}
	var plans []struct {
		Plan measuredNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		q.t.Fatalf("read the plan: %v", err)
	}
	var walk func(n measuredNode)
	walk = func(n measuredNode) {
		if n.Relation != "" {
			q.rows[n.Relation] += int64((n.Rows + n.Removed) * max(n.Loops, 1))
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
}

// measureRows runs read with every statement measured and returns how many
// rows of each relation it touched.
func measureRows(t *testing.T, read func(w *Watch) error) map[string]int64 {
	t.Helper()
	touched := map[string]int64{}
	watch := NewWatch(testPool)
	watch.wrap = func(inner storage.Querier) storage.Querier {
		return measuringQuerier{Querier: inner, t: t, rows: touched}
	}
	if err := read(watch); err != nil {
		t.Fatalf("read: %v", err)
	}
	return touched
}

// TestWatchReadsScanNoJournal runs every organiser's read against a database
// holding a representative olympiad — three contests of forty participants,
// each with a few hundred queries, events, answers and sign-ins, beside
// everything else the test database holds — and EXPLAINs each statement
// exactly as the read sends it: every node that reads query_log,
// participant_events, submissions or audit_log must be an index scan with an
// index condition — a range — never a sequential scan or a walk of a whole
// index (design §9).
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
		analyzeForPlans(t, ctx)

		f := contests[1]
		var reg uuid.UUID
		if err := q.QueryRow(ctx, `SELECT id FROM registrations WHERE contest_id = $1 LIMIT 1`, f.contest).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		var scans []string
		ordered := false
		watch := NewWatch(testPool)
		watch.wrap = func(inner storage.Querier) storage.Querier {
			return explainingQuerier{Querier: inner, t: t, scans: &scans, ordered: &ordered}
		}
		// The keyset reads: their query log must come out of the index in
		// order.
		//
		// Only where the range is longer than the page: a page that takes a
		// registration's whole remaining range is rightly a bitmap scan and a
		// sort, and so is a page filtered so narrowly that the planner
		// expects to read the range to fill it.
		keyset := map[string]bool{"export sources": true}
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
			"answers": func() error { _, err := watch.Answers(ctx, f.contest, reg, monitor.MaxAttemptQueries); return err },
			"export sources": func() error {
				for _, source := range []monitor.Source{monitor.SourceQuery, monitor.SourceAnswer} {
					start := monitor.Cursor{At: monitor.EarliestCursorTime, Source: monitor.SourceAudit, ID: "0"}
					if _, err := watch.FeedSource(ctx, monitor.FeedQuery{Contest: f.contest, Registration: reg,
						After: &start, Until: time.Now(), Limit: 50}, source); err != nil {
						return err
					}
				}
				return nil
			},
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
			scans, ordered = scans[:0], keyset[name]
			if err := read(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, scan := range scans {
				t.Errorf("%s reads a journal out of a range or out of order: %s", name, scan)
			}
		}
	})
}

// analyzeForPlans gathers the statistics the planner chooses on, so that the
// plan a test sees is the plan production would run on data of this shape.
func analyzeForPlans(t *testing.T, ctx context.Context) {
	t.Helper()
	q := storage.QuerierFrom(ctx, testPool)
	for _, table := range []string{"users", "registrations", "query_log", "participant_events",
		"submissions", "audit_log", "workspace_revisions", "registration_activity"} {
		if _, err := q.Exec(ctx, "ANALYZE "+table); err != nil {
			t.Fatalf("analyze %s: %v", table, err)
		}
	}
}

// moreHistory gives every participant already enrolled in f's contest another
// round of what they do: queries queries, twenty events and one answer to a
// question of this round, all after everything already there. Rounds are an
// hour apart, so the times of one never meet another's.
func moreHistory(t *testing.T, f *watchFixture, round, queries int) {
	t.Helper()
	question := f.makeQuestion(round)
	start := f.base.Add(time.Duration(round) * time.Hour)
	f.exec(`
		INSERT INTO query_log (registration_id, request_id, sql_text, status, ip, sql_fingerprint, executed_at)
		SELECT r.id, gen_random_uuid(),
		       'select * from suspects where id = ' || n || ' and name like ''%' || md5(n::text) || '%''',
		       (ARRAY['ok','ok','ok','error','rejected'])[1 + n % 5], '192.0.2.1', n % 50,
		       $2::timestamptz + n * interval '10 seconds'
		FROM registrations r CROSS JOIN generate_series(1, $3::int) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, start, queries)
	f.exec(`
		INSERT INTO participant_events (contest_id, registration_id, kind, payload, created_at)
		SELECT $1, r.id, CASE WHEN n % 2 = 0 THEN 'page_left' ELSE 'paste' END,
		       CASE WHEN n % 2 = 0 THEN '{"away_ms": 4000}'::jsonb
		            ELSE '{"target": "editor", "chars": 300, "text": "x"}'::jsonb END,
		       $2::timestamptz + n * interval '30 seconds'
		FROM registrations r CROSS JOIN generate_series(1, 20) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, start)
	f.exec(`
		INSERT INTO submissions (registration_id, question_id, attempt_no, value, is_correct, submitted_at)
		SELECT r.id, $2, 1, 'v', true, $3::timestamptz + interval '30 minutes'
		FROM registrations r
		WHERE r.contest_id = $1`, f.contest, question, start)
}

// loadOlympiad enrols participants in f's contest and gives each three
// hundred queries, a hundred events, sixty answers, fifty sign-ins, ten
// failed ones and twenty revisions — written in time order across the
// participants, as a real olympiad interleaves them on disk.
func loadOlympiad(t *testing.T, f *watchFixture, tag, participants int) {
	t.Helper()
	loadOlympiadQueries(t, f, tag, participants, 300)
}

// loadOlympiadQueries is loadOlympiad with queries queries per participant.
func loadOlympiadQueries(t *testing.T, f *watchFixture, tag, participants, queries int) {
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
		FROM registrations r CROSS JOIN generate_series(1, $3::int) n
		WHERE r.contest_id = $1
		ORDER BY n, r.id`, f.contest, f.base, queries)
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
		SELECT r.id, $2, n, 'v', n = 60, $3::timestamptz + n * interval '150 seconds'
		FROM registrations r CROSS JOIN generate_series(1, 60) n
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
