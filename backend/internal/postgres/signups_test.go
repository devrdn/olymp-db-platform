package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/signup"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// signupClock is a fixed instant the tests measure expiry against; the store
// is told the time rather than reading it.
var signupClock = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// signupTag sets this run's rows apart, so a row another package's test
// committed can neither collide with them nor be counted as theirs.
var signupTag = uuid.NewString()[:8]

func addr(local string) string   { return local + "." + signupTag + "@example.edu" }
func handle(login string) string { return login + "." + signupTag }

func pendingSignup(email, login string) signup.Pending {
	return signup.Pending{
		Email: email, Login: login, FullName: "Ana Popescu", PasswordHash: "argon2id-digest",
		CodeHash: bytes.Repeat([]byte{7}, 48), TokenHash: bytes.Repeat([]byte{3}, 32),
		SentAt: signupClock, ExpiresAt: signupClock.Add(signup.CodeLifetime),
	}
}

// waiting is Waiting with the email aside, failing the test on an error. A
// test compares it with what it was before the test's own rows, inside
// withSnapshot.
func waiting(t *testing.T, ctx context.Context, repo *Signups, email string, now time.Time) int {
	t.Helper()
	n, err := repo.Waiting(ctx, email, now)
	if err != nil {
		t.Fatalf("Waiting() = %v", err)
	}
	return n
}

func takeSignup(t *testing.T, ctx context.Context, repo *Signups, email string) signup.Pending {
	t.Helper()
	p, ok, err := repo.Take(ctx, email)
	if err != nil || !ok {
		t.Fatalf("Take(%s) = %v, %v; want the sign-up", email, ok, err)
	}
	return p
}

// withSnapshot is withTx in one REPEATABLE READ snapshot, so a row another
// package's test commits meanwhile cannot shift a count between a baseline
// and the assertion made against it.
func withSnapshot(t *testing.T, body func(ctx context.Context)) {
	t.Helper()
	withTx(t, func(ctx context.Context) {
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
			t.Fatalf("take a snapshot: %v", err)
		}
		body(ctx)
	})
}

// signupCheckRefused fails t unless err is the named CHECK constraint refusing a
// row.
func signupCheckRefused(t *testing.T, err error, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != checkViolation || pgErr.ConstraintName != constraint {
		t.Errorf("error = %v, want %s refusing the row (SQLSTATE 23514)", err, constraint)
	}
}

func TestSignupsPutStoresAndReplacesByEmail(t *testing.T) {
	withSnapshot(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		before := waiting(t, ctx, repo, "", signupClock)
		if err := repo.Put(ctx, pendingSignup(addr("ana"), handle("ana")), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}
		first := takeSignup(t, ctx, repo, addr("ana"))
		want := pendingSignup(addr("ana"), handle("ana"))
		if first.ID == uuid.Nil || first.Login != want.Login || first.FullName != want.FullName ||
			first.PasswordHash != want.PasswordHash || !bytes.Equal(first.CodeHash, want.CodeHash) ||
			!bytes.Equal(first.TokenHash, want.TokenHash) || first.Tries != 0 ||
			!first.SentAt.Equal(want.SentAt) || !first.ExpiresAt.Equal(want.ExpiresAt) {
			t.Errorf("stored %+v, want %+v", first, want)
		}
		if err := repo.CountTry(ctx, first.ID); err != nil {
			t.Fatalf("CountTry() = %v", err)
		}

		again := pendingSignup(addr("ana"), handle("ana2"))
		again.SentAt = signupClock.Add(time.Minute)
		again.TokenHash = bytes.Repeat([]byte{4}, 32)
		if err := repo.Put(ctx, again, signup.MaxPending, signupClock.Add(time.Minute)); err != nil {
			t.Fatalf("Put() again = %v", err)
		}
		second := takeSignup(t, ctx, repo, addr("ana"))
		if second.Login != handle("ana2") || second.Tries != 0 || !second.SentAt.Equal(again.SentAt) ||
			!bytes.Equal(second.TokenHash, again.TokenHash) {
			t.Errorf("replaced %+v, want the second form and token with no tries", second)
		}
		if n := waiting(t, ctx, repo, "", signupClock); n != before+1 {
			t.Errorf("Waiting() = %d, want one row for the email beyond the %d before", n, before)
		}
	})
}

// A refusal must not abort the caller's transaction: the next statement in it
// still runs.
func TestSignupsPutRefusesAHeldLoginAndTheCap(t *testing.T) {
	withSnapshot(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		capacity := waiting(t, ctx, repo, "", signupClock) + 2
		for _, p := range []signup.Pending{pendingSignup(addr("ana"), handle("ana")), pendingSignup(addr("bob"), handle("bob"))} {
			if err := repo.Put(ctx, p, capacity, signupClock); err != nil {
				t.Fatalf("Put(%s) = %v", p.Email, err)
			}
		}

		if err := repo.Put(ctx, pendingSignup(addr("eve"), handle("ANA")), signup.MaxPending, signupClock); !errors.Is(err, users.ErrLoginTaken) {
			t.Errorf("a held login: Put() = %v, want ErrLoginTaken", err)
		}
		if err := repo.Put(ctx, pendingSignup(addr("eve"), handle("eve")), capacity, signupClock); !errors.Is(err, signup.ErrFull) {
			t.Errorf("past the cap: Put() = %v, want ErrFull", err)
		}
		if err := repo.Put(ctx, pendingSignup(addr("ana"), handle("ana")), capacity, signupClock); err != nil {
			t.Errorf("replacing one's own sign-up at the cap: Put() = %v", err)
		}
		if n := waiting(t, ctx, repo, addr("bob"), signupClock); n != capacity-1 {
			t.Errorf("Waiting() after the refusals = %d, want the transaction alive and bob's own row aside", n)
		}
	})
}

func TestSignupsAnExpiredSignUpHoldsNothing(t *testing.T) {
	withSnapshot(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		later := signupClock.Add(signup.CodeLifetime)
		beforeLater := waiting(t, ctx, repo, "", later)
		if err := repo.Put(ctx, pendingSignup(addr("ana"), handle("ana")), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}

		if held, err := repo.LoginHeld(ctx, handle("ANA"), addr("eve"), signupClock); err != nil || !held {
			t.Errorf("LoginHeld() while live = %v, %v; want held", held, err)
		}
		if held, err := repo.LoginHeld(ctx, handle("ana"), addr("ana"), signupClock); err != nil || held {
			t.Errorf("LoginHeld() by the email's own sign-up = %v, %v; want free", held, err)
		}
		if held, err := repo.LoginHeld(ctx, handle("ana"), addr("eve"), later); err != nil || held {
			t.Errorf("LoginHeld() once expired = %v, %v; want free", held, err)
		}
		if n := waiting(t, ctx, repo, "", later); n != beforeLater {
			t.Errorf("Waiting() once expired = %d, want the %d of before: the expired row does not count", n, beforeLater)
		}

		if err := repo.Put(ctx, pendingSignup(addr("eve"), handle("Ana")), beforeLater+1, later); err != nil {
			t.Fatalf("Put() over an expired holder = %v", err)
		}
		if _, ok, err := repo.Take(ctx, addr("ana")); err != nil || ok {
			t.Errorf("Take(the expired holder) = %v, %v; want it removed", ok, err)
		}
	})
}

func TestSignupsCountTryRenewAndDelete(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		if err := repo.Put(ctx, pendingSignup(addr("ana"), handle("ana")), signup.MaxPending, signupClock); err != nil {
			t.Fatalf("Put() = %v", err)
		}
		p := takeSignup(t, ctx, repo, addr("ana"))
		for range 2 {
			if err := repo.CountTry(ctx, p.ID); err != nil {
				t.Fatalf("CountTry() = %v", err)
			}
		}
		if got := takeSignup(t, ctx, repo, addr("ana")).Tries; got != 2 {
			t.Errorf("tries = %d, want 2", got)
		}

		code, sent := bytes.Repeat([]byte{9}, 48), signupClock.Add(2*time.Minute)
		if err := repo.Renew(ctx, p.ID, code, sent, sent.Add(signup.CodeLifetime)); err != nil {
			t.Fatalf("Renew() = %v", err)
		}
		renewed := takeSignup(t, ctx, repo, addr("ana"))
		if renewed.Tries != 2 || !bytes.Equal(renewed.CodeHash, code) || !renewed.SentAt.Equal(sent) ||
			!renewed.ExpiresAt.Equal(sent.Add(signup.CodeLifetime)) || !bytes.Equal(renewed.TokenHash, p.TokenHash) {
			t.Errorf("renewed %+v, want the new code and times, the tries and the token kept", renewed)
		}

		if err := repo.Delete(ctx, p.ID); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
		if _, ok, err := repo.Take(ctx, addr("ana")); err != nil || ok {
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
		if _, err := repo.Waiting(ctx, "", signupClock); err != nil {
			t.Errorf("Waiting() after it = %v; want the transaction alive", err)
		}
	})
}

func TestSignupsSweepRemovesOnlyExpiredRows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewSignups(testPool)
		old := pendingSignup(addr("old"), handle("old"))
		live := pendingSignup(addr("live"), handle("live"))
		live.ExpiresAt = live.ExpiresAt.Add(time.Second)
		for _, p := range []signup.Pending{old, live} {
			if err := repo.Put(ctx, p, signup.MaxPending, signupClock); err != nil {
				t.Fatalf("Put(%s) = %v", p.Email, err)
			}
		}

		// Other packages' expired rows may be swept too, so the count is a
		// lower bound and this run's rows are checked one by one.
		swept, err := repo.Sweep(ctx, old.ExpiresAt)
		if err != nil || swept < 1 {
			t.Fatalf("Sweep() = %d, %v; want at least the expired row", swept, err)
		}
		if _, ok, _ := repo.Take(ctx, addr("old")); ok {
			t.Error("the expired sign-up was not swept")
		}
		if _, ok, _ := repo.Take(ctx, addr("live")); !ok {
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
		err := NewSignups(testPool).Put(ctx, pendingSignup(addr("Ana"), handle("ana")), signup.MaxPending, signupClock)
		if _, rollbackErr := q.Exec(ctx, `ROLLBACK TO SAVEPOINT uppercase`); rollbackErr != nil {
			t.Fatalf("rollback to the savepoint: %v", rollbackErr)
		}
		signupCheckRefused(t, err, "pending_signups_email_check")
	})
}

func TestTheTableRefusesAHashOfTheWrongLength(t *testing.T) {
	for name, edit := range map[string]func(*signup.Pending){
		"pending_signups_code_hash_check":  func(p *signup.Pending) { p.CodeHash = p.CodeHash[:47] },
		"pending_signups_token_hash_check": func(p *signup.Pending) { p.TokenHash = append(p.TokenHash, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				p := pendingSignup(addr("ana"), handle("ana"))
				edit(&p)
				signupCheckRefused(t, NewSignups(testPool).Put(ctx, p, signup.MaxPending, signupClock), name)
			})
		})
	}
}

// The check judges ASCII letters only, which every collation lowercases alike;
// beyond them PostgreSQL's lower() follows the database's collation and could
// refuse what Go's lowercasing stored.
func TestTheEmailCheckDoesNotDependOnTheCollation(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		if err := NewSignups(testPool).Put(ctx, pendingSignup(addr("Élise"), handle("elise")), signup.MaxPending, signupClock); err != nil {
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
