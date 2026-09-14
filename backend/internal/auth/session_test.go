package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/google/uuid"
)

// spyCache records the keys it is asked about, so a test can assert on what
// actually reaches the store.
type spyCache struct {
	cache.Cache
	keys []string
}

func newSpyCache() *spyCache {
	return &spyCache{Cache: cache.NewMemory(100)}
}

func (s *spyCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.keys = append(s.keys, key)
	return s.Cache.Set(ctx, key, value, ttl)
}

func newTestStore(t *testing.T) (*SessionStore, cache.Cache) {
	t.Helper()
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	return NewSessionStore(c, time.Hour), c
}

func testPrincipal() Principal {
	return Principal{
		UserID:     uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Login:      "ivanov",
		Generation: 1,
	}
}

func TestCreateReturnsARetrievableSession(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	token, err := store.Create(ctx, testPrincipal())
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	got, err := store.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	if got.UserID != testPrincipal().UserID {
		t.Errorf("UserID = %v, want %v", got.UserID, testPrincipal().UserID)
	}
	if got.Login != "ivanov" {
		t.Errorf("Login = %q, want ivanov", got.Login)
	}
	if got.Generation != 1 {
		t.Errorf("Generation = %d, want 1", got.Generation)
	}
}

func TestCreateIssuesAUniqueHighEntropyToken(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	first, _ := store.Create(ctx, testPrincipal())
	second, _ := store.Create(ctx, testPrincipal())

	if first == second {
		t.Error("two sessions received the same token")
	}
	// 32 random bytes in base64url; anything shorter would be guessable.
	if len(first) < 43 {
		t.Errorf("token %q is %d characters, too short to resist guessing", first, len(first))
	}
}

func TestTokenIsNotStoredVerbatim(t *testing.T) {
	// Whoever reads the cache — a dump, a misconfigured Redis — must not come
	// away with usable session tokens.
	spy := newSpyCache()
	t.Cleanup(func() { _ = spy.Close() })
	store := NewSessionStore(spy, time.Hour)

	token, err := store.Create(context.Background(), testPrincipal())
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	for _, key := range spy.keys {
		if strings.Contains(key, token) {
			t.Errorf("cache key %q contains the raw token", key)
		}
	}
}

func TestGetReportsAnUnknownToken(t *testing.T) {
	store, _ := newTestStore(t)

	_, err := store.Get(context.Background(), "not-a-real-token")

	if err == nil {
		t.Fatal("Get() accepted an unknown token")
	}
	if err != ErrSessionNotFound {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestGetReportsAnEmptyToken(t *testing.T) {
	store, _ := newTestStore(t)

	if _, err := store.Get(context.Background(), ""); err == nil {
		t.Error("Get() accepted an empty token")
	}
}

func TestDeleteEndsTheSession(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	if err := store.Delete(ctx, token); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	if _, err := store.Get(ctx, token); err != ErrSessionNotFound {
		t.Error("session survived Delete; logout did not end it")
	}
}

func TestDeleteIsQuietAboutAnUnknownToken(t *testing.T) {
	// Logging out twice, or with a stale cookie, is not an error worth
	// surfacing to the user.
	store, _ := newTestStore(t)

	if err := store.Delete(context.Background(), "unknown"); err != nil {
		t.Errorf("Delete() on an unknown token = %v, want nil", err)
	}
}

func TestSessionExpiresAfterItsLifetime(t *testing.T) {
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, 20*time.Millisecond)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	time.Sleep(50 * time.Millisecond)

	if _, err := store.Get(ctx, token); err != ErrSessionNotFound {
		t.Error("session outlived its TTL")
	}
}

func TestRefreshExtendsAnActiveSession(t *testing.T) {
	// Sliding expiry: someone working through a contest must not be logged out
	// mid-answer.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, 80*time.Millisecond)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	time.Sleep(50 * time.Millisecond)
	if err := store.Refresh(ctx, token); err != nil {
		t.Fatalf("Refresh() returned error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if _, err := store.Get(ctx, token); err != nil {
		t.Errorf("session expired although it was refreshed: %v", err)
	}
}

func TestRefreshReportsAnUnknownToken(t *testing.T) {
	store, _ := newTestStore(t)

	if err := store.Refresh(context.Background(), "unknown"); err != ErrSessionNotFound {
		t.Errorf("Refresh() on an unknown token = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionRecordsWhenItWasIssued(t *testing.T) {
	store, _ := newTestStore(t)
	before := time.Now().Add(-time.Second)

	token, _ := store.Create(context.Background(), testPrincipal())
	got, _ := store.Get(context.Background(), token)

	if got.IssuedAt.Before(before) {
		t.Errorf("IssuedAt = %v, want a time at or after %v", got.IssuedAt, before)
	}
}

func TestTouchLeavesARecentlyRefreshedSessionAlone(t *testing.T) {
	// Authenticating is one read; it must not become a write as well on every
	// request when the last write was moments ago.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())
	before, _ := store.Get(ctx, token)

	if err := store.Touch(ctx, token, before); err != nil {
		t.Fatalf("Touch() returned error: %v", err)
	}

	after, _ := store.Get(ctx, token)
	if !after.RefreshedAt.Equal(before.RefreshedAt) {
		t.Errorf("RefreshedAt moved from %v to %v; a session refreshed moments ago was rewritten", before.RefreshedAt, after.RefreshedAt)
	}
}

func TestTouchExtendsASessionThatIsDue(t *testing.T) {
	// The sliding window is still the promise: once the interval has passed,
	// activity buys a full lifetime again.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, 80*time.Millisecond) // the interval is 8ms
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	time.Sleep(50 * time.Millisecond)
	session, err := store.Get(ctx, token)
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	if err := store.Touch(ctx, token, session); err != nil {
		t.Fatalf("Touch() returned error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	if _, err := store.Get(ctx, token); err != nil {
		t.Errorf("session expired although it was touched: %v", err)
	}
}

func TestTouchTreatsARecordWithoutARefreshTimeAsDue(t *testing.T) {
	// Sessions written before the field existed carry no RefreshedAt. They
	// are extended on their next request rather than left to lapse.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())
	legacy, _ := store.Get(ctx, token)
	legacy.RefreshedAt = time.Time{}
	legacy.IssuedAt = time.Now().Add(-2 * time.Hour)

	if err := store.Touch(ctx, token, legacy); err != nil {
		t.Fatalf("Touch() returned error: %v", err)
	}

	after, _ := store.Get(ctx, token)
	if after.RefreshedAt.IsZero() {
		t.Error("a legacy record was not stamped with a refresh time")
	}
}

// ageSession rewrites a stored session as if it had been issued age ago and
// kept in use ever since — the record a stolen cookie kept warm would have.
func ageSession(t *testing.T, c cache.Cache, token string, age time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	record, err := json.Marshal(Session{
		UserID:      testPrincipal().UserID,
		Login:       testPrincipal().Login,
		Generation:  testPrincipal().Generation,
		IssuedAt:    now.Add(-age),
		RefreshedAt: now,
	})
	if err != nil {
		t.Fatalf("encode session: %v", err)
	}
	if err := c.Set(context.Background(), sessionKey(token), record, time.Hour); err != nil {
		t.Fatalf("store session: %v", err)
	}
}

func TestASessionOlderThanItsMaximumLifetimeIsRefusedAndRemoved(t *testing.T) {
	// The idle timeout slides on every request, so on its own it never ends a
	// session somebody keeps using — including somebody using a copied cookie.
	// The maximum lifetime counts from sign-in and nothing extends it.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour).WithMaxLifetime(12 * time.Hour)
	ctx := context.Background()
	token, err := store.Create(ctx, testPrincipal())
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	ageSession(t, c, token, 12*time.Hour+time.Minute)

	if _, err := store.Get(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Get() of a session past its maximum lifetime = %v, want ErrSessionNotFound", err)
	}
	if _, found, _ := c.Get(ctx, sessionKey(token)); found {
		t.Error("the expired session was left in the store")
	}
}

func TestASessionWithinItsMaximumLifetimeIsStillValid(t *testing.T) {
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour).WithMaxLifetime(12 * time.Hour)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())
	ageSession(t, c, token, 11*time.Hour)

	if _, err := store.Get(ctx, token); err != nil {
		t.Errorf("Get() of a session within its maximum lifetime = %v, want it valid", err)
	}
}

func TestActivityNeverExtendsASessionPastItsMaximumLifetime(t *testing.T) {
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour).WithMaxLifetime(120 * time.Millisecond)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if session, err := store.Get(ctx, token); err == nil {
			_ = store.Refresh(ctx, token)
			_ = store.Touch(ctx, token, session)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := store.Get(ctx, token); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("Get() after the maximum lifetime of constant use = %v, want ErrSessionNotFound", err)
	}
}

func TestAStoredSessionExpiresFromTheStoreAtItsMaximumLifetime(t *testing.T) {
	// The record is written with no more time to live than the session has
	// left, so a session nobody asks about again does not sit in the store
	// for a full idle timeout past its end.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	store := NewSessionStore(c, time.Hour).WithMaxLifetime(40 * time.Millisecond)
	ctx := context.Background()
	token, _ := store.Create(ctx, testPrincipal())

	time.Sleep(80 * time.Millisecond)

	if _, found, _ := c.Get(ctx, sessionKey(token)); found {
		t.Error("the record outlived the session's maximum lifetime in the store")
	}
}
