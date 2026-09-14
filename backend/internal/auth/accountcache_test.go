package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// switchableCache passes through to an in-process store until failing is set,
// and then fails every read and write — an unreachable Redis, in effect. It
// also records the lifetime of every write.
type switchableCache struct {
	cache.Cache

	mu      sync.Mutex
	failing bool
	sets    []time.Duration
}

var errCacheDown = errors.New("cache is unreachable")

func newSwitchableCache(t *testing.T) *switchableCache {
	t.Helper()
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	return &switchableCache{Cache: c}
}

func (s *switchableCache) fail(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = on
}

func (s *switchableCache) down() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failing
}

func (s *switchableCache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.down() {
		return nil, false, errCacheDown
	}
	return s.Cache.Get(ctx, key)
}

func (s *switchableCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.down() {
		return errCacheDown
	}
	s.mu.Lock()
	s.sets = append(s.sets, ttl)
	s.mu.Unlock()
	return s.Cache.Set(ctx, key, value, ttl)
}

func (s *switchableCache) setCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sets)
}

func newTestAccountCache(t *testing.T, ttl time.Duration) (*AccountCache, *switchableCache) {
	t.Helper()
	c := newSwitchableCache(t)
	return NewAccountCache(c, ttl, logging.New("error", io.Discard)), c
}

func testAccount() users.User {
	return users.User{
		ID:                 uuid.New(),
		Login:              "ivanov",
		Email:              "ivanov@example.org",
		FullName:           "Ivan Ivanov",
		Status:             users.StatusActive,
		PasswordHash:       "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		SessionGeneration:  3,
		MustChangePassword: true,
		Roles:              []string{"student"},
		Permissions:        []string{"contest.participate", "results.view"},
	}
}

func TestAStoredAccountIsReadBackWithWhatAuthenticationDecidesOn(t *testing.T) {
	accounts, _ := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	want := testAccount()

	if _, hit, slot := accounts.lookup(ctx, want.ID); hit {
		t.Fatal("an empty cache reported a hit")
	} else {
		accounts.store(ctx, slot, want)
	}

	got, hit, _ := accounts.lookup(ctx, want.ID)
	if !hit {
		t.Fatal("the stored account was not found")
	}
	if got.ID != want.ID || got.Login != want.Login || got.Status != want.Status ||
		got.SessionGeneration != want.SessionGeneration || got.MustChangePassword != want.MustChangePassword ||
		!slices.Equal(got.Permissions, want.Permissions) {
		t.Errorf("read back %+v, want the fields of %+v", got, want)
	}
}

func TestTheCachedAccountCarriesNoCredentialOrPersonalDetail(t *testing.T) {
	// The copy is read by nothing but the middleware; the digest and the
	// contact details have no business sitting in a shared store for it.
	accounts, c := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	account := testAccount()

	_, _, slot := accounts.lookup(ctx, account.ID)
	accounts.store(ctx, slot, account)

	raw, found, err := c.Cache.Get(ctx, slot.key)
	if err != nil || !found {
		t.Fatalf("the entry is not under its slot's key: found=%v err=%v", found, err)
	}
	for _, secret := range []string{account.PasswordHash, account.Email, account.FullName} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("the cached entry holds %q: %s", secret, raw)
		}
	}
}

func TestForgetMakesTheCachedAccountUnreachable(t *testing.T) {
	accounts, _ := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	account := testAccount()
	_, _, slot := accounts.lookup(ctx, account.ID)
	accounts.store(ctx, slot, account)

	accounts.Forget(ctx, []uuid.UUID{account.ID})

	if _, hit, _ := accounts.lookup(ctx, account.ID); hit {
		t.Error("the account was still served from the cache after Forget")
	}
}

func TestForgetOutrunsARequestThatReadTheAccountBeforeTheChange(t *testing.T) {
	// The race a plain delete loses: a request reads the old row, the change
	// commits and the entry is deleted, and then that request writes the old
	// row back. Its write lands under the generation it started with, which
	// Forget has already replaced, so nobody reads it.
	accounts, _ := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	stale := testAccount()

	_, _, slot := accounts.lookup(ctx, stale.ID) // the in-flight request starts
	accounts.Forget(ctx, []uuid.UUID{stale.ID})  // the change commits
	accounts.store(ctx, slot, stale)             // the request caches what it read

	if _, hit, _ := accounts.lookup(ctx, stale.ID); hit {
		t.Error("an account read before the change was served after it")
	}
}

func TestForgetReachesEveryNamedAccountAndNoOther(t *testing.T) {
	accounts, _ := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	first, second, bystander := testAccount(), testAccount(), testAccount()
	for _, a := range []users.User{first, second, bystander} {
		_, _, slot := accounts.lookup(ctx, a.ID)
		accounts.store(ctx, slot, a)
	}

	accounts.Forget(ctx, []uuid.UUID{first.ID, second.ID})

	for _, a := range []users.User{first, second} {
		if _, hit, _ := accounts.lookup(ctx, a.ID); hit {
			t.Errorf("%v was still cached after Forget named it", a.ID)
		}
	}
	if _, hit, _ := accounts.lookup(ctx, bystander.ID); !hit {
		t.Error("Forget dropped an account it did not name")
	}
}

func TestACachedAccountIsNeverServedForAnotherUser(t *testing.T) {
	accounts, c := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	owner, other := testAccount(), testAccount()

	_, _, slot := accounts.lookup(ctx, owner.ID)
	accounts.store(ctx, slot, owner)
	if _, hit, _ := accounts.lookup(ctx, other.ID); hit {
		t.Fatal("one account's entry answered a lookup for another")
	}

	// An entry that somehow landed under the wrong key — a bug elsewhere, a
	// hand-edited store — names its own user, and is refused for anyone else.
	_, _, otherSlot := accounts.lookup(ctx, other.ID)
	raw, _, _ := c.Cache.Get(ctx, slot.key)
	if err := c.Cache.Set(ctx, otherSlot.key, raw, time.Minute); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}
	if got, hit, _ := accounts.lookup(ctx, other.ID); hit {
		t.Errorf("an entry for %v was served for %v", got.ID, other.ID)
	}
}

func TestAGarbledEntryIsAMissThatTheNextReadRepairs(t *testing.T) {
	accounts, c := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	account := testAccount()
	_, _, slot := accounts.lookup(ctx, account.ID)
	if err := c.Cache.Set(ctx, slot.key, []byte(`{"user_id":`), time.Minute); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	_, hit, repair := accounts.lookup(ctx, account.ID)
	if hit {
		t.Fatal("a garbled entry was served")
	}
	accounts.store(ctx, repair, account)
	if _, hit, _ := accounts.lookup(ctx, account.ID); !hit {
		t.Error("the entry read from the database did not replace the garbled one")
	}
}

func TestAGarbledGenerationIsAMissAndIsNotWrittenUnder(t *testing.T) {
	accounts, c := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	account := testAccount()
	if err := c.Cache.Set(ctx, accountGenerationKey(account.ID), []byte("not a generation"), time.Minute); err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	_, hit, slot := accounts.lookup(ctx, account.ID)
	if hit {
		t.Fatal("a lookup under a garbled generation reported a hit")
	}
	before := c.setCount()
	accounts.store(ctx, slot, account)
	if c.setCount() != before {
		t.Error("an entry was written under a generation that could not be read")
	}
}

func TestAnUnreachableCacheIsAMissThatWritesNothing(t *testing.T) {
	// Failing safe here means falling back to the database, never serving
	// something half-read and never guessing at a generation.
	accounts, c := newTestAccountCache(t, 5*time.Second)
	ctx := context.Background()
	account := testAccount()
	_, _, slot := accounts.lookup(ctx, account.ID)
	accounts.store(ctx, slot, account)

	c.fail(true)
	_, hit, failedSlot := accounts.lookup(ctx, account.ID)
	if hit {
		t.Fatal("a lookup against an unreachable cache reported a hit")
	}
	c.fail(false)
	before := c.setCount()
	accounts.store(ctx, failedSlot, account)
	if c.setCount() != before {
		t.Error("a slot from a failed lookup was written")
	}
}

func TestAFailedForgetIsLogged(t *testing.T) {
	c := newSwitchableCache(t)
	var logs bytes.Buffer
	accounts := NewAccountCache(c, 5*time.Second, logging.New("error", &logs))
	c.fail(true)

	accounts.Forget(context.Background(), []uuid.UUID{uuid.New()})

	if !strings.Contains(logs.String(), "cached account") {
		t.Errorf("a Forget that reached no cache left no error in the log: %q", logs.String())
	}
}

func TestTheEntryLivesForTheConfiguredLifetimeAndNoLonger(t *testing.T) {
	const ttl = 40 * time.Millisecond
	accounts, c := newTestAccountCache(t, ttl)
	ctx := context.Background()
	account := testAccount()

	_, _, slot := accounts.lookup(ctx, account.ID)
	accounts.store(ctx, slot, account)
	if got := c.sets[len(c.sets)-1]; got != ttl {
		t.Errorf("entry written for %v, want %v", got, ttl)
	}
	if _, hit, _ := accounts.lookup(ctx, account.ID); !hit {
		t.Fatal("the entry was gone at once")
	}

	time.Sleep(3 * ttl)

	if _, hit, _ := accounts.lookup(ctx, account.ID); hit {
		t.Error("the entry outlived its lifetime")
	}
}

func TestTheGenerationOutlivesAnyEntryWrittenBeforeIt(t *testing.T) {
	// When a generation lapses the account reads the first one again, so an
	// entry written under the first must be long gone by then.
	accounts, c := newTestAccountCache(t, 5*time.Second)
	accounts.Forget(context.Background(), []uuid.UUID{uuid.New()})

	if got := c.sets[len(c.sets)-1]; got < 100*accounts.ttl {
		t.Errorf("generation written for %v, want far longer than an entry's %v", got, accounts.ttl)
	}
}
