package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

func outboxMessage(to string, user uuid.UUID) mailing.Message {
	return mailing.Message{
		Kind:    mailing.KindSignInDetails,
		UserID:  user,
		To:      to,
		Subject: "Datele de autentificare",
		Body:    "Login: ana\nParola: Zx8-q2LmT0pa4RwK\n",
	}
}

// outboxRow is what a test reads back about a message.
type outboxRow struct {
	body                *string
	attempts            int
	sent, abandoned     bool
	lastError           *string
	nextAttemptAfterNow time.Duration
	lastAttemptAt       *time.Time
	userID              *uuid.UUID
}

func readOutboxRow(t *testing.T, ctx context.Context, id int64) outboxRow {
	t.Helper()
	var row outboxRow
	var seconds float64
	err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `
		SELECT body, attempts, sent_at IS NOT NULL, abandoned_at IS NOT NULL, last_error,
		       extract(epoch FROM next_attempt_at - now()), last_attempt_at, user_id
		FROM mail_outbox WHERE id = $1`, id).Scan(
		&row.body, &row.attempts, &row.sent, &row.abandoned, &row.lastError,
		&seconds, &row.lastAttemptAt, &row.userID)
	if err != nil {
		t.Fatalf("read message %d: %v", id, err)
	}
	row.nextAttemptAfterNow = time.Duration(seconds * float64(time.Second))
	return row
}

// claimOne claims through the repository and fails the test on an error.
func claimOne(t *testing.T, ctx context.Context, perMinute int) (mailing.Queued, bool) {
	t.Helper()
	q, ok, err := NewMailOutbox(testPool).Claim(ctx, perMinute, mailing.ClaimLease)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return q, ok
}

func TestAQueuedMessageIsClaimedWithEverythingNeededToSendIt(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		user := makeUser(t, ctx, "outbox-"+uuid.NewString()[:8])
		repo := NewMailOutbox(testPool)
		want := outboxMessage("ana@usm.md", user.ID)
		if err := repo.Insert(ctx, want); err != nil {
			t.Fatalf("insert: %v", err)
		}

		q, ok := claimOne(t, ctx, 25)
		if !ok {
			t.Fatal("the queued message was not claimed")
		}
		if q.Message != want {
			t.Errorf("claimed %+v, want %+v", q.Message, want)
		}
		if q.Attempts != 1 || q.ID == 0 || !q.CreatedAt.Equal(txNow(t, ctx)) {
			t.Errorf("claimed id %d attempts %d created %s; want an id, one attempt, created now",
				q.ID, q.Attempts, q.CreatedAt)
		}

		// The lease keeps it from every other claim until it ends.
		row := readOutboxRow(t, ctx, q.ID)
		if row.nextAttemptAfterNow != mailing.ClaimLease {
			t.Errorf("leased until now+%s, want now+%s", row.nextAttemptAfterNow, mailing.ClaimLease)
		}
		if _, again := claimOne(t, ctx, 25); again {
			t.Error("a leased message was claimed a second time")
		}
	})
}

func TestAMessageForNoAccountKeepsNoUser(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		if err := repo.Insert(ctx, outboxMessage("ana@usm.md", uuid.Nil)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		q, ok := claimOne(t, ctx, 25)
		if !ok || q.UserID != uuid.Nil {
			t.Fatalf("claimed %+v (ok %v), want a message with no user", q, ok)
		}
		if row := readOutboxRow(t, ctx, q.ID); row.userID != nil {
			t.Errorf("user_id = %v, want NULL", *row.userID)
		}
	})
}

// The rate is counted in the table, so every replica spends one budget.
func TestNoMoreAreClaimedInAMinuteThanTheRateAllows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		for i := range 3 {
			if err := repo.Insert(ctx, outboxMessage("p"+string(rune('a'+i))+"@usm.md", uuid.Nil)); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}

		for i := range 2 {
			if _, ok := claimOne(t, ctx, 2); !ok {
				t.Fatalf("claim %d of 2 within the rate found nothing", i+1)
			}
		}
		if q, ok := claimOne(t, ctx, 2); ok {
			t.Fatalf("a third message (%d) was claimed within a minute at two a minute", q.ID)
		}

		// A minute later the budget is free again.
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE mail_outbox SET last_attempt_at = last_attempt_at - interval '61 seconds'`); err != nil {
			t.Fatalf("age the claims: %v", err)
		}
		if _, ok := claimOne(t, ctx, 2); !ok {
			t.Error("the third message was not claimed once the minute had passed")
		}
	})
}

func TestTheOldestDueMessageIsClaimedFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		for _, to := range []string{"first@usm.md", "second@usm.md"} {
			if err := repo.Insert(ctx, outboxMessage(to, uuid.Nil)); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE mail_outbox SET next_attempt_at = now() - interval '1 hour' WHERE recipient = 'second@usm.md'`); err != nil {
			t.Fatalf("make the second message due earlier: %v", err)
		}

		q, ok := claimOne(t, ctx, 25)
		if !ok || q.To != "second@usm.md" {
			t.Fatalf("claimed %q, want the message due first", q.To)
		}
	})
}

// committedMessage stores a message outside any test transaction, as a
// replica's claim needs to see it, and deletes it when the test ends.
func committedMessage(t *testing.T, to string) int64 {
	t.Helper()
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	var id int64
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO mail_outbox (kind, recipient, subject, body)
		VALUES ('sign_in_details', $1, 'Subject', 'Body') RETURNING id`, to).Scan(&id); err != nil {
		t.Fatalf("store a message: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM mail_outbox WHERE id = $1`, id)
	})
	return id
}

// Another replica's claim holds its row locked until it commits; this claim
// passes over it to the next one instead of waiting or taking it too.
func TestAClaimSkipsAMessageAnotherSessionHoldsLocked(t *testing.T) {
	held := committedMessage(t, "held@usm.md")
	free := committedMessage(t, "free@usm.md")

	other, err := testPool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	if _, err := other.Exec(t.Context(), `SELECT id FROM mail_outbox WHERE id = $1 FOR UPDATE`, held); err != nil {
		t.Fatalf("lock the first message: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	q, ok := claimOne(t, ctx, 25)
	if !ok || q.ID != free {
		t.Fatalf("claimed %d (ok %v), want %d, the message nobody holds", q.ID, ok, free)
	}
}

// Claims take turns, so two replicas cannot both count the minute's budget
// before either has spent it.
func TestAClaimTakesNothingWhileAnotherReplicaIsClaiming(t *testing.T) {
	committedMessage(t, "due@usm.md")

	other, err := testPool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = other.Rollback(context.Background()) }()
	if _, err := other.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(mailOutboxLockKey)); err != nil {
		t.Fatalf("take the claim lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if q, ok := claimOne(t, ctx, 25); ok {
		t.Fatalf("claimed %d while another replica held the claim lock", q.ID)
	}
}

func TestASentMessageLosesItsBody(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		if err := repo.Insert(ctx, outboxMessage("ana@usm.md", uuid.Nil)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		q, _ := claimOne(t, ctx, 25)

		if err := repo.MarkSent(ctx, q.ID); err != nil {
			t.Fatalf("mark sent: %v", err)
		}
		row := readOutboxRow(t, ctx, q.ID)
		if !row.sent || row.body != nil {
			t.Fatalf("sent %v, body %v; want a sent message without its body", row.sent, row.body)
		}
		if err := repo.MarkSent(ctx, q.ID); err == nil {
			t.Error("a message was marked sent twice")
		}
		if n, err := repo.CountDue(ctx); err != nil || n != 0 {
			t.Errorf("CountDue() = %d, %v; a sent message is not due", n, err)
		}
	})
}

func TestAnAbandonedMessageLosesItsBodyAndKeepsTheReason(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		if err := repo.Insert(ctx, outboxMessage("ana@usm.md", uuid.Nil)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		q, _ := claimOne(t, ctx, 25)

		if err := repo.Abandon(ctx, q.ID, "mail: permanent: 550 5.1.1 no such mailbox"); err != nil {
			t.Fatalf("abandon: %v", err)
		}
		row := readOutboxRow(t, ctx, q.ID)
		if !row.abandoned || row.sent || row.body != nil {
			t.Fatalf("abandoned %v, sent %v, body %v; want abandoned without its body", row.abandoned, row.sent, row.body)
		}
		if row.lastError == nil || *row.lastError != "mail: permanent: 550 5.1.1 no such mailbox" {
			t.Errorf("last_error = %v", row.lastError)
		}
		if _, again := claimOne(t, ctx, 25); again {
			t.Error("an abandoned message was claimed")
		}
	})
}

func TestARetryMovesTheNextAttemptAndKeepsTheBody(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		if err := repo.Insert(ctx, outboxMessage("ana@usm.md", uuid.Nil)); err != nil {
			t.Fatalf("insert: %v", err)
		}
		q, _ := claimOne(t, ctx, 25)

		if err := repo.Retry(ctx, q.ID, 4*time.Minute, "mail: temporary: 421 4.7.0 try later"); err != nil {
			t.Fatalf("retry: %v", err)
		}
		row := readOutboxRow(t, ctx, q.ID)
		if row.nextAttemptAfterNow != 4*time.Minute {
			t.Errorf("next attempt at now+%s, want now+4m", row.nextAttemptAfterNow)
		}
		if row.body == nil || row.sent || row.abandoned || row.attempts != 1 {
			t.Errorf("body %v sent %v abandoned %v attempts %d; want a pending message with its body after one attempt",
				row.body, row.sent, row.abandoned, row.attempts)
		}
		if row.lastError == nil || *row.lastError != "mail: temporary: 421 4.7.0 try later" {
			t.Errorf("last_error = %v", row.lastError)
		}
		if _, early := claimOne(t, ctx, 25); early {
			t.Error("a message waiting for its retry was claimed early")
		}

		// Due again: the next claim counts a second attempt.
		if err := repo.Retry(ctx, q.ID, 0, "shutdown"); err != nil {
			t.Fatalf("retry now: %v", err)
		}
		if again, ok := claimOne(t, ctx, 25); !ok || again.Attempts != 2 {
			t.Errorf("claimed again with %d attempts (ok %v), want 2", again.Attempts, ok)
		}
	})
}

func TestOnlyPendingMessagesWhoseTimeHasComeAreDue(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewMailOutbox(testPool)
		for _, to := range []string{"due@usm.md", "later@usm.md", "sent@usm.md"} {
			if err := repo.Insert(ctx, outboxMessage(to, uuid.Nil)); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `UPDATE mail_outbox SET next_attempt_at = now() + interval '1 hour' WHERE recipient = 'later@usm.md'`); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(ctx, `UPDATE mail_outbox SET sent_at = now(), body = NULL WHERE recipient = 'sent@usm.md'`); err != nil {
			t.Fatal(err)
		}

		if n, err := NewMailOutbox(testPool).CountDue(ctx); err != nil || n != 1 {
			t.Fatalf("CountDue() = %d, %v; want 1", n, err)
		}
	})
}

// The erasure and the bounds hold in the table as well as in the code that
// writes it.
func TestTheTableRefusesAFinishedMessageWithABodyAndAnUnboundedError(t *testing.T) {
	for name, statement := range map[string]string{
		"sent with its body":      `UPDATE mail_outbox SET sent_at = now() WHERE id = $1`,
		"abandoned with its body": `UPDATE mail_outbox SET abandoned_at = now() WHERE id = $1`,
		"pending without a body":  `UPDATE mail_outbox SET body = NULL WHERE id = $1`,
		"sent and abandoned":      `UPDATE mail_outbox SET sent_at = now(), abandoned_at = now(), body = NULL WHERE id = $1`,
		"an unbounded error":      `UPDATE mail_outbox SET last_error = repeat('e', 501) WHERE id = $1`,
		"an oversized body":       `UPDATE mail_outbox SET body = repeat('b', 65537) WHERE id = $1`,
	} {
		t.Run(name, func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				if err := NewMailOutbox(testPool).Insert(ctx, outboxMessage("ana@usm.md", uuid.Nil)); err != nil {
					t.Fatalf("insert: %v", err)
				}
				q := storage.QuerierFrom(ctx, testPool)
				var id int64
				if err := q.QueryRow(ctx, `SELECT max(id) FROM mail_outbox`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				_, err := q.Exec(ctx, `SAVEPOINT refused`)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := q.Exec(ctx, statement, id); err == nil || !strings.Contains(err.Error(), "23514") {
					t.Errorf("%s was accepted (%v), want a check violation", name, err)
				}
				_, _ = q.Exec(ctx, `ROLLBACK TO SAVEPOINT refused`)
			})
		})
	}
}
