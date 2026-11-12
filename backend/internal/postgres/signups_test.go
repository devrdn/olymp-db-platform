package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/signup"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// signupClock is a fixed instant the tests measure expiry against; the store
// is told the time rather than reading it.
var signupClock = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func pendingSignup(email, login string) signup.Pending {
	return signup.Pending{
		Email: email, Login: login, FullName: "Ana Popescu", PasswordHash: "argon2id-digest",
		CodeHash: bytes.Repeat([]byte{7}, 48), SentAt: signupClock, ExpiresAt: signupClock.Add(signup.CodeLifetime),
	}
}

func takeSignup(t *testing.T, ctx context.Context, repo *Signups, email string) signup.Pending {
	t.Helper()
	p, ok, err := repo.Take(ctx, email)
	if err != nil || !ok {
		t.Fatalf("Take(%s) = %v, %v; want the sign-up", email, ok, err)
	}
	return p
}

func TestSignupsPutStoresAndReplacesByEmail(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		if err := repo.Put(ctx, pendingSignup("ana@example.edu", "ana"), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}
		first := takeSignup(t, ctx, repo, "ana@example.edu")
		want := pendingSignup("ana@example.edu", "ana")
		if first.ID == uuid.Nil || first.Login != want.Login || first.FullName != want.FullName ||
			first.PasswordHash != want.PasswordHash || !bytes.Equal(first.CodeHash, want.CodeHash) || first.Tries != 0 ||
			!first.SentAt.Equal(want.SentAt) || !first.ExpiresAt.Equal(want.ExpiresAt) {
			t.Errorf("stored %+v, want %+v", first, want)
		}
		if err := repo.CountTry(ctx, first.ID); err != nil {
			t.Fatalf("CountTry() = %v", err)
		}

		again := pendingSignup("ana@example.edu", "ana2")
		again.SentAt = signupClock.Add(time.Minute)
		if err := repo.Put(ctx, again, signup.MaxPending, signupClock.Add(time.Minute)); err != nil {
			t.Fatalf("Put() again = %v", err)
		}
		second := takeSignup(t, ctx, repo, "ana@example.edu")
		if second.Login != "ana2" || second.Tries != 0 || !second.SentAt.Equal(again.SentAt) {
			t.Errorf("replaced %+v, want the second form with no tries", second)
		}
		if n, err := repo.Waiting(ctx, "", signupClock); err != nil || n != 1 {
			t.Errorf("Waiting() = %d, %v; want one row for the email", n, err)
		}
	})
}

// A refusal must not abort the caller's transaction: the next statement in it
// still runs.
func TestSignupsPutRefusesAHeldLoginAndTheCap(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		for _, p := range []signup.Pending{pendingSignup("ana@example.edu", "ana"), pendingSignup("bob@example.edu", "bob")} {
			if err := repo.Put(ctx, p, 2, signupClock); err != nil {
				t.Fatalf("Put(%s) = %v", p.Email, err)
			}
		}

		if err := repo.Put(ctx, pendingSignup("eve@example.edu", "ANA"), signup.MaxPending, signupClock); !errors.Is(err, users.ErrLoginTaken) {
			t.Errorf("a held login: Put() = %v, want ErrLoginTaken", err)
		}
		if err := repo.Put(ctx, pendingSignup("eve@example.edu", "eve"), 2, signupClock); !errors.Is(err, signup.ErrFull) {
			t.Errorf("past the cap: Put() = %v, want ErrFull", err)
		}
		if err := repo.Put(ctx, pendingSignup("ana@example.edu", "ana"), 2, signupClock); err != nil {
			t.Errorf("replacing one's own sign-up at the cap: Put() = %v", err)
		}
		if n, err := repo.Waiting(ctx, "bob@example.edu", signupClock); err != nil || n != 1 {
			t.Errorf("Waiting() after the refusals = %d, %v; want the transaction alive and bob's own row aside", n, err)
		}
	})
}

func TestSignupsAnExpiredSignUpHoldsNothing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		if err := repo.Put(ctx, pendingSignup("ana@example.edu", "ana"), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}
		later := signupClock.Add(signup.CodeLifetime)

		if held, err := repo.LoginHeld(ctx, "ANA", "eve@example.edu", signupClock); err != nil || !held {
			t.Errorf("LoginHeld() while live = %v, %v; want held", held, err)
		}
		if held, err := repo.LoginHeld(ctx, "ana", "ana@example.edu", signupClock); err != nil || held {
			t.Errorf("LoginHeld() by the email's own sign-up = %v, %v; want free", held, err)
		}
		if held, err := repo.LoginHeld(ctx, "ana", "eve@example.edu", later); err != nil || held {
			t.Errorf("LoginHeld() once expired = %v, %v; want free", held, err)
		}
		if n, err := repo.Waiting(ctx, "", later); err != nil || n != 0 {
			t.Errorf("Waiting() once expired = %d, %v; want none", n, err)
		}

		if err := repo.Put(ctx, pendingSignup("eve@example.edu", "Ana"), 1, later); err != nil {
			t.Fatalf("Put() over an expired holder = %v", err)
		}
		if _, ok, err := repo.Take(ctx, "ana@example.edu"); err != nil || ok {
			t.Errorf("Take(the expired holder) = %v, %v; want it removed", ok, err)
		}
	})
}

func TestSignupsCountTryRenewAndDelete(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		if err := repo.Put(ctx, pendingSignup("ana@example.edu", "ana"), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}
		p := takeSignup(t, ctx, repo, "ana@example.edu")
		for range 2 {
			if err := repo.CountTry(ctx, p.ID); err != nil {
				t.Fatalf("CountTry() = %v", err)
			}
		}
		if got := takeSignup(t, ctx, repo, "ana@example.edu").Tries; got != 2 {
			t.Errorf("tries = %d, want 2", got)
		}

		code, sent := bytes.Repeat([]byte{9}, 48), signupClock.Add(2*time.Minute)
		if err := repo.Renew(ctx, p.ID, code, sent, sent.Add(signup.CodeLifetime)); err != nil {
			t.Fatalf("Renew() = %v", err)
		}
		renewed := takeSignup(t, ctx, repo, "ana@example.edu")
		if renewed.Tries != 0 || !bytes.Equal(renewed.CodeHash, code) || !renewed.SentAt.Equal(sent) ||
			!renewed.ExpiresAt.Equal(sent.Add(signup.CodeLifetime)) {
			t.Errorf("renewed %+v, want the new code and times and no tries", renewed)
		}

		if err := repo.Delete(ctx, p.ID); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		if _, ok, err := repo.Take(ctx, "ana@example.edu"); err != nil || ok {
			t.Errorf("Take() after Delete = %v, %v; want none", ok, err)
		}
	})
}

func TestSignupsTakeOfAnAddressNoRowCanHoldIsNone(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		if _, ok, err := repo.Take(ctx, "bad\x00@example.edu"); err != nil || ok {
			t.Errorf("Take(a NUL address) = %v, %v; want none", ok, err)
		}
		if n, err := repo.Waiting(ctx, "", signupClock); err != nil || n != 0 {
			t.Errorf("Waiting() after it = %d, %v; want the transaction alive", n, err)
		}
	})
}

func TestSignupsSweepRemovesOnlyExpiredRows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		old := pendingSignup("old@example.edu", "old")
		live := pendingSignup("live@example.edu", "live")
		live.ExpiresAt = live.ExpiresAt.Add(time.Second)
		for _, p := range []signup.Pending{old, live} {
			if err := repo.Put(ctx, p, signup.MaxPending, signupClock); err != nil {
				t.Fatalf("Put(%s) = %v", p.Email, err)
			}
		}

		swept, err := repo.Sweep(ctx, old.ExpiresAt)
		if err != nil || swept != 1 {
			t.Fatalf("Sweep() = %d, %v; want the expired row", swept, err)
		}
		if _, ok, _ := repo.Take(ctx, "live@example.edu"); !ok {
			t.Error("the live sign-up was swept")
		}
	})
}

func TestTheTableRefusesAnEmailTheDomainWouldNotStore(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `SAVEPOINT uppercase`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		err := NewSignups(testPool).Put(ctx, pendingSignup("Ana@example.edu", "ana"), signup.MaxPending, signupClock)
		if _, rollbackErr := q.Exec(ctx, `ROLLBACK TO SAVEPOINT uppercase`); rollbackErr != nil {
			t.Fatalf("rollback to the savepoint: %v", rollbackErr)
		}
		if err == nil {
			t.Error("the table stored an email that is not lowercase")
		}
	})
}

// The check judges ASCII letters only, which every collation lowercases alike;
// beyond them PostgreSQL's lower() follows the database's collation and could
// refuse what Go's lowercasing stored.
func TestTheEmailCheckDoesNotDependOnTheCollation(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		if err := NewSignups(testPool).Put(ctx, pendingSignup("Élise@example.edu", "elise"), signup.MaxPending, signupClock); err != nil {
			t.Errorf("Put() of an address with a non-ASCII capital = %v, want the check to leave it to the domain", err)
		}
	})
}

// Two confirmations of one code must not both find the row: the second waits
// for the first's transaction and then finds nothing.
func TestSignupsTakeLocksTheRowUntilTheTransactionEnds(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := t.Context()
	repo := NewSignups(testPool)
	uow := storage.NewUnitOfWork(testPool)
	email := "lock-" + uuid.NewString()[:8] + "@example.edu"
	if err := repo.Put(ctx, pendingSignup(email, "lock-"+uuid.NewString()[:8]), signup.MaxPending, signupClock); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM pending_signups WHERE email = $1`, email)
	})

	taken := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- uow.Do(ctx, func(ctx context.Context) error {
			p, ok, err := repo.Take(ctx, email)
			if err != nil || !ok {
				close(taken)
				return errors.Join(err, errors.New("the first Take found nothing"))
			}
			close(taken)
			<-release
			return repo.Delete(ctx, p.ID)
		})
	}()
	<-taken

	second := make(chan bool, 1)
	secondErr := make(chan error, 1)
	go func() {
		secondErr <- uow.Do(ctx, func(ctx context.Context) error {
			_, ok, err := repo.Take(ctx, email)
			second <- ok
			return err
		})
	}()

	select {
	case <-second:
		t.Fatal("the second Take returned while the first transaction held the row")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	if ok := <-second; ok {
		t.Error("the second Take found the row the first transaction spent")
	}
	if err := <-secondErr; err != nil {
		t.Errorf("second transaction: %v", err)
	}
}
