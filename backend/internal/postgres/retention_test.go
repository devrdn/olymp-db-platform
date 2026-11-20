package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// contestWithJournal makes a contest and n query_log rows for it.
func contestWithJournal(t *testing.T, ctx context.Context, n int) uuid.UUID {
	t.Helper()

	user := makeUser(t, ctx, "retention-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, user.ID)
	registration := makeRegistration(t, ctx, contest, user.ID)
	for range n {
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO query_log (registration_id, request_id, sql_text) VALUES ($1, $2, 'SELECT 1')`,
			registration, uuid.New()); err != nil {
			t.Fatalf("insert a journal row: %v", err)
		}
	}
	return contest
}

func journalRows(t *testing.T, ctx context.Context, contest uuid.UUID) int {
	t.Helper()

	var n int
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT count(*) FROM query_log WHERE contest_id = $1`, contest).Scan(&n); err != nil {
		t.Fatalf("count journal rows: %v", err)
	}
	return n
}

func setContest(t *testing.T, ctx context.Context, contest uuid.UUID, status, archivedAge string) {
	t.Helper()

	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = $2 WHERE id = $1`, contest, status); err != nil {
		t.Fatalf("set the status: %v", err)
	}
	if archivedAge == "" {
		return
	}
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET archived_at = now() - $2::interval WHERE id = $1`, contest, archivedAge); err != nil {
		t.Fatalf("set archived_at: %v", err)
	}
}

func TestRetentionDeletesTheJournalOfAContestArchivedOverAYearAgo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		old := contestWithJournal(t, ctx, 3)
		recent := contestWithJournal(t, ctx, 3)
		finished := contestWithJournal(t, ctx, 3)
		setContest(t, ctx, old, "archived", "13 months")
		setContest(t, ctx, recent, "archived", "11 months")
		setContest(t, ctx, finished, "finished", "")
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE contests SET updated_at = now() - interval '2 years' WHERE id = $1`, finished); err != nil {
			t.Fatal(err)
		}

		n, err := NewRetention(testPool).DeleteQueryLog(ctx, 12, 5000)
		if err != nil || n != 3 {
			t.Fatalf("DeleteQueryLog() = %d, %v, want 3", n, err)
		}
		for contest, want := range map[uuid.UUID]int{old: 0, recent: 3, finished: 3} {
			if got := journalRows(t, ctx, contest); got != want {
				t.Errorf("contest %s keeps %d rows, want %d", contest, got, want)
			}
		}
	})
}

func TestRetentionKeepsTheJournalOfAContestThatIsNoLongerArchived(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := contestWithJournal(t, ctx, 2)
		setContest(t, ctx, contest, "archived", "13 months")
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE contests SET status = 'finished' WHERE id = $1`, contest); err != nil {
			t.Fatal(err)
		}

		n, err := NewRetention(testPool).DeleteQueryLog(ctx, 12, 5000)
		if err != nil || n != 0 {
			t.Fatalf("DeleteQueryLog() = %d, %v, want 0", n, err)
		}
		if got := journalRows(t, ctx, contest); got != 2 {
			t.Errorf("journal rows = %d, want 2", got)
		}
	})
}

func TestRetentionDeletesTheJournalInBatches(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := contestWithJournal(t, ctx, 5)
		setContest(t, ctx, contest, "archived", "13 months")
		store := NewRetention(testPool)

		for i, want := range []int{2, 2, 1, 0} {
			n, err := store.DeleteQueryLog(ctx, 12, 2)
			if err != nil || n != want {
				t.Fatalf("batch %d = %d, %v, want %d", i, n, err, want)
			}
		}
	})
}

func TestRetentionDeletesAuditEntriesOlderThanEighteenMonths(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		old := appendAged(t, ctx, "19 months")
		young := appendAged(t, ctx, "17 months")

		n, err := NewRetention(testPool).DeleteAuditLog(ctx, 18, 5000)
		if err != nil || n < 1 {
			t.Fatalf("DeleteAuditLog() = %d, %v, want the old entry gone", n, err)
		}
		var oldLeft, youngLeft int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FILTER (WHERE id = $1), count(*) FILTER (WHERE id = $2)
			 FROM audit_log`, old, young).Scan(&oldLeft, &youngLeft); err != nil {
			t.Fatal(err)
		}
		if oldLeft != 0 || youngLeft != 1 {
			t.Errorf("old kept %d, young kept %d, want 0 and 1", oldLeft, youngLeft)
		}
	})
}

func TestPruningTheTrailTakesNoTableLock(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		appendAged(t, ctx, "19 months")
		if _, err := NewRetention(testPool).DeleteAuditLog(ctx, 18, 5000); err != nil {
			t.Fatalf("DeleteAuditLog() = %v", err)
		}

		rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx,
			`SELECT mode FROM pg_locks
			 WHERE relation = 'audit_log'::regclass AND pid = pg_backend_pid()`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var mode string
			if err := rows.Scan(&mode); err != nil {
				t.Fatal(err)
			}
			if mode != "RowExclusiveLock" && mode != "AccessShareLock" {
				t.Errorf("lock %s held on audit_log, want only row-level modes", mode)
			}
		}
	})
}

func insertMail(t *testing.T, ctx context.Context, set string) int64 {
	t.Helper()

	var id int64
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`INSERT INTO mail_outbox (kind, recipient, subject, body, created_at, sent_at, abandoned_at)
		 SELECT 'test', 'a@example.org', 's', CASE WHEN f.sent IS NULL AND f.ab IS NULL THEN 'b' END,
		        now() - interval '60 days', f.sent, f.ab
		 FROM (SELECT `+set+`) AS f(sent, ab)
		 RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert mail (%s): %v", set, err)
	}
	return id
}

func TestRetentionDeletesMailFinishedOverThirtyDaysAgo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		sent := insertMail(t, ctx, `now() - interval '31 days', NULL::timestamptz`)
		abandoned := insertMail(t, ctx, `NULL::timestamptz, now() - interval '31 days'`)
		recent := insertMail(t, ctx, `now() - interval '29 days', NULL::timestamptz`)

		n, err := NewRetention(testPool).DeleteMailOutbox(ctx, 30, 5000)
		if err != nil || n < 2 {
			t.Fatalf("DeleteMailOutbox() = %d, %v, want at least 2", n, err)
		}
		var left []int64
		rows, err := storage.QuerierFrom(ctx, testPool).Query(ctx,
			`SELECT id FROM mail_outbox WHERE id = ANY($1)`, []int64{sent, abandoned, recent})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			left = append(left, id)
		}
		if len(left) != 1 || left[0] != recent {
			t.Errorf("rows left = %v, want only %d", left, recent)
		}
	})
}

func TestRetentionKeepsAMessageThatMayStillBeSent(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		pending := insertMail(t, ctx, `NULL::timestamptz, NULL::timestamptz`)

		if _, err := NewRetention(testPool).DeleteMailOutbox(ctx, 30, 5000); err != nil {
			t.Fatalf("DeleteMailOutbox() = %v", err)
		}
		var n int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM mail_outbox WHERE id = $1`, pending).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("the pending message was deleted")
		}
	})
}
