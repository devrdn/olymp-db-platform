package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// resetClock is a fixed instant the tests measure expiry against; the store
// is told the time rather than reading it.
var resetClock = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// resetLink is a link for account with a fresh random token hash, so no two
// tests, and no two runs, share a key.
func resetLink(t *testing.T, account uuid.UUID) recovery.Link {
	t.Helper()
	hash := make([]byte, 32)
	if _, err := rand.Read(hash); err != nil {
		t.Fatal(err)
	}
	return recovery.Link{
		TokenHash: hash, UserID: account, Email: "ana@example.edu",
		ExpiresAt: resetClock.Add(recovery.LinkLifetime),
	}
}

func replaceLink(t *testing.T, ctx context.Context, repo *PasswordResets, l recovery.Link) {
	t.Helper()
	if err := repo.Replace(ctx, l); err != nil {
		t.Fatalf("Replace() = %v", err)
	}
}

func linkWorks(t *testing.T, ctx context.Context, repo *PasswordResets, l recovery.Link, now time.Time) bool {
	t.Helper()
	ok, err := repo.Find(ctx, l.TokenHash, now)
	if err != nil {
		t.Fatalf("Find() = %v", err)
	}
	return ok
}

func TestPasswordResetsSpendReturnsTheLinkOnce(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		account := makeUser(t, ctx, "reset-"+uuid.NewString()[:8])
		l := resetLink(t, account.ID)
		replaceLink(t, ctx, repo, l)

		got, ok, err := repo.Spend(ctx, l.TokenHash)
		if err != nil || !ok {
			t.Fatalf("Spend() = %v, %v; want the link", ok, err)
		}
		if !bytes.Equal(got.TokenHash, l.TokenHash) || got.UserID != l.UserID || got.Email != l.Email ||
			!got.ExpiresAt.Equal(l.ExpiresAt) {
			t.Errorf("spent %+v, want %+v", got, l)
		}
		if _, ok, err := repo.Spend(ctx, l.TokenHash); err != nil || ok {
			t.Errorf("Spend() again = %v, %v; want nothing", ok, err)
		}
	})
}

func TestPasswordResetsReplaceKeepsOneLinkPerAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		account := makeUser(t, ctx, "reset-"+uuid.NewString()[:8])
		other := makeUser(t, ctx, "reset-"+uuid.NewString()[:8])
		old, fresh, others := resetLink(t, account.ID), resetLink(t, account.ID), resetLink(t, other.ID)
		for _, l := range []recovery.Link{old, others, fresh} {
			replaceLink(t, ctx, repo, l)
		}

		if linkWorks(t, ctx, repo, old, resetClock) {
			t.Error("the replaced link still works")
		}
		if !linkWorks(t, ctx, repo, fresh, resetClock) || !linkWorks(t, ctx, repo, others, resetClock) {
			t.Error("the latest link, or another account's, does not work")
		}
		var rows int
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT count(*) FROM password_resets WHERE user_id = $1`, account.ID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Errorf("%d links for the account, want one", rows)
		}
	})
}

func TestPasswordResetsFindOnlyAnUnexpiredLink(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		l := resetLink(t, makeUser(t, ctx, "reset-"+uuid.NewString()[:8]).ID)
		replaceLink(t, ctx, repo, l)

		if !linkWorks(t, ctx, repo, l, l.ExpiresAt.Add(-time.Second)) {
			t.Error("the link does not work a second before it expires")
		}
		if linkWorks(t, ctx, repo, l, l.ExpiresAt) {
			t.Error("the link works at its expiry")
		}
		if linkWorks(t, ctx, repo, resetLink(t, uuid.New()), resetClock) {
			t.Error("an unknown token works")
		}
	})
}

func TestPasswordResetsSweepRemovesOnlyExpiredRows(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		old := resetLink(t, makeUser(t, ctx, "reset-"+uuid.NewString()[:8]).ID)
		live := resetLink(t, makeUser(t, ctx, "reset-"+uuid.NewString()[:8]).ID)
		live.ExpiresAt = live.ExpiresAt.Add(time.Second)
		replaceLink(t, ctx, repo, old)
		replaceLink(t, ctx, repo, live)

		// Other packages' expired rows may be swept too, so the count is a
		// lower bound and this run's rows are checked one by one.
		swept, err := repo.Sweep(ctx, old.ExpiresAt)
		if err != nil || swept < 1 {
			t.Fatalf("Sweep() = %d, %v; want at least the expired row", swept, err)
		}
		if _, ok, _ := repo.Spend(ctx, old.TokenHash); ok {
			t.Error("the expired link was not swept")
		}
		if _, ok, _ := repo.Spend(ctx, live.TokenHash); !ok {
			t.Error("the live link was swept")
		}
	})
}

func TestPasswordResetsGoWithTheirAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		account := makeUser(t, ctx, "reset-"+uuid.NewString()[:8])
		l := resetLink(t, account.ID)
		replaceLink(t, ctx, repo, l)

		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `DELETE FROM users WHERE id = $1`, account.ID); err != nil {
			t.Fatalf("delete the account: %v", err)
		}
		if _, ok, _ := repo.Spend(ctx, l.TokenHash); ok {
			t.Error("the link outlived its account")
		}
	})
}

func TestPasswordResetsDeleteForRemovesOnlyTheNamedAccountsLinks(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewPasswordResets(testPool)
		named, other := makeUser(t, ctx, "reset-"+uuid.NewString()[:8]), makeUser(t, ctx, "reset-"+uuid.NewString()[:8])
		gone, kept := resetLink(t, named.ID), resetLink(t, other.ID)
		replaceLink(t, ctx, repo, gone)
		replaceLink(t, ctx, repo, kept)

		if err := repo.DeleteFor(ctx, []uuid.UUID{named.ID}); err != nil {
			t.Fatalf("DeleteFor() = %v", err)
		}

		if _, ok, _ := repo.Spend(ctx, gone.TokenHash); ok {
			t.Error("the named account's link survived")
		}
		if _, ok, _ := repo.Spend(ctx, kept.TokenHash); !ok {
			t.Error("another account's link was deleted")
		}
	})
}

func TestThePasswordResetsTableRefusesATokenHashOfAnotherLength(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		q := storage.QuerierFrom(ctx, testPool)
		if _, err := q.Exec(ctx, `SAVEPOINT short`); err != nil {
			t.Fatalf("savepoint: %v", err)
		}
		l := resetLink(t, makeUser(t, ctx, "reset-"+uuid.NewString()[:8]).ID)
		l.TokenHash = l.TokenHash[:16]
		err := NewPasswordResets(testPool).Replace(ctx, l)
		if _, rollbackErr := q.Exec(ctx, `ROLLBACK TO SAVEPOINT short`); rollbackErr != nil {
			t.Fatalf("rollback to the savepoint: %v", rollbackErr)
		}
		signupCheckRefused(t, err, "password_resets_token_hash_check")
	})
}

// Two completions of one link race in their own transactions, as two
// requests do: only one of them may spend it.
func TestPasswordResetsTwoSpendsOfOneLinkFindItOnce(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := context.Background()
	account, err := NewUsers(testPool).Create(ctx, users.User{
		Login: "reset-race-" + uuid.NewString()[:8], FullName: "Race", Status: users.StatusActive, PasswordHash: "x",
	})
	if err != nil {
		t.Fatalf("create the account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, account.ID)
	})
	repo := NewPasswordResets(testPool)
	l := resetLink(t, account.ID)
	replaceLink(t, ctx, repo, l)

	var wg sync.WaitGroup
	var mu sync.Mutex
	spent := 0
	for range 2 {
		wg.Go(func() {
			err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
				_, ok, err := repo.Spend(ctx, l.TokenHash)
				if ok {
					mu.Lock()
					spent++
					mu.Unlock()
				}
				return err
			})
			if err != nil {
				t.Errorf("Spend() = %v", err)
			}
		})
	}
	wg.Wait()

	if spent != 1 {
		t.Errorf("the link was spent %d times, want once", spent)
	}
}
