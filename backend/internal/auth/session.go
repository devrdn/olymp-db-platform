// Package auth answers "who is this?": password sign-in, server-side sessions
// and their cookie, brute-force throttling, and the middleware that turns a
// session into an identity. It does not answer "may they do this?" (rbac) and
// does not own accounts (users).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

const (
	tokenBytes = 32

	sessionKeyPrefix = "sess:"
)

// ErrSessionNotFound reports a token that is unknown, expired or withdrawn.
var ErrSessionNotFound = errors.New("session not found")

// UserStore is the slice of users.Repository authentication needs (CLAUDE.md
// layout rule 3).
type UserStore interface {
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
	ByLogin(ctx context.Context, login string) (users.User, error)
	SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error
}

// Principal is the identity a session is issued for.
type Principal struct {
	UserID uuid.UUID
	Login  string
	// Generation pins the session to the account's session generation;
	// blocking or a password change advances it, retiring older sessions.
	Generation int64
}

type Session struct {
	UserID     uuid.UUID `json:"user_id"`
	Login      string    `json:"login"`
	Generation int64     `json:"generation"`
	IssuedAt   time.Time `json:"issued_at"`
	// RefreshedAt is when the lifetime was last rewritten; zero is treated
	// as due.
	RefreshedAt time.Time `json:"refreshed_at,omitzero"`
}

// maxRefreshInterval caps how long an active session goes without its
// lifetime being rewritten (CLAUDE.md rule 6).
const maxRefreshInterval = time.Minute

// DefaultMaxSessionLifetime is a working day: longer than any contest, short
// enough that a copied cookie kept warm does not last for days.
const DefaultMaxSessionLifetime = 12 * time.Hour

// SessionStore keeps server-side sessions in the shared cache (per instance on
// the in-process fallback), so access can be withdrawn at once.
//
// A session ends at the first of two limits: the idle timeout, which slides
// with activity, and the maximum lifetime from sign-in, which nothing extends,
// so a copied cookie kept in use still ends.
type SessionStore struct {
	cache       cache.Cache
	ttl         time.Duration
	maxLifetime time.Duration
}

// NewSessionStore returns a store whose sessions idle out after ttl and never
// outlive DefaultMaxSessionLifetime.
func NewSessionStore(c cache.Cache, ttl time.Duration) *SessionStore {
	return &SessionStore{cache: c, ttl: ttl, maxLifetime: DefaultMaxSessionLifetime}
}

// WithMaxLifetime sets the maximum lifetime; non-positive keeps the default.
// For the composition root, before the store is shared.
func (s *SessionStore) WithMaxLifetime(d time.Duration) *SessionStore {
	if d > 0 {
		s.maxLifetime = d
	}
	return s
}

// Create issues a session and returns its token; the store keeps only a
// digest.
func (s *SessionStore) Create(ctx context.Context, p Principal) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now().UTC()
	if err := s.put(ctx, token, Session{
		UserID:      p.UserID,
		Login:       p.Login,
		Generation:  p.Generation,
		IssuedAt:    now,
		RefreshedAt: now,
	}); err != nil {
		return "", err
	}

	return token, nil
}

// put writes a session for the idle timeout or its remaining lifetime,
// whichever is shorter, so the store drops it at its end.
func (s *SessionStore) put(ctx context.Context, token string, session Session) error {
	ttl := min(s.ttl, s.remaining(session, time.Now()))
	if ttl <= 0 {
		// Past its end: writing would resurrect a dead record.
		return ErrSessionNotFound
	}
	record, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	if err := s.cache.Set(ctx, sessionKey(token), record, ttl); err != nil {
		return fmt.Errorf("store session: %w", err)
	}
	return nil
}

func (s *SessionStore) Get(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionNotFound
	}

	raw, found, err := s.cache.Get(ctx, sessionKey(token))
	if err != nil {
		// Fail closed.
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	if !found {
		return Session{}, ErrSessionNotFound
	}

	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}

	// Past its maximum lifetime the session is over and removed (its expiry
	// removes it anyway). A record without an issue time is refused too.
	if s.remaining(session, time.Now()) <= 0 {
		_ = s.cache.Delete(ctx, sessionKey(token))
		return Session{}, ErrSessionNotFound
	}

	return session, nil
}

// SessionAlive reports, for monitor.Tracker, whether the tracked session (by
// digest) could still be in use beside the current one: it exists, is within
// its lifetime, and has not been retired by a newer generation of the same
// account. A block is not checked: a blocked account makes no second request.
func (s *SessionStore) SessionAlive(ctx context.Context, tracked, current string) (bool, error) {
	if !isDigest(tracked) {
		return false, nil
	}
	old, found, err := s.byDigest(ctx, tracked)
	if err != nil || !found {
		return false, err
	}
	if s.remaining(old, time.Now()) <= 0 {
		return false, nil
	}
	if isDigest(current) {
		now, found, err := s.byDigest(ctx, current)
		if err != nil {
			return false, err
		}
		if found && now.UserID == old.UserID && now.Generation > old.Generation {
			return false, nil
		}
	}
	return true, nil
}

func (s *SessionStore) byDigest(ctx context.Context, digest string) (Session, bool, error) {
	raw, found, err := s.cache.Get(ctx, sessionKeyPrefix+digest)
	if err != nil || !found {
		return Session{}, false, err
	}
	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return Session{}, false, fmt.Errorf("decode session: %w", err)
	}
	return session, true, nil
}

func isDigest(value string) bool {
	if len(value) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (s *SessionStore) remaining(session Session, now time.Time) time.Duration {
	if session.IssuedAt.IsZero() {
		return 0
	}
	return session.IssuedAt.Add(s.maxLifetime).Sub(now)
}

// Touch extends a session the caller already loaded, only when the last
// extension is older than the refresh interval, so the common request costs
// one read (CLAUDE.md rule 6). The idle timeout is honoured to within one
// interval.
func (s *SessionStore) Touch(ctx context.Context, token string, session Session) error {
	now := time.Now().UTC()

	last := session.RefreshedAt
	if last.IsZero() {
		// Without RefreshedAt, the issue time is the last known refresh.
		last = session.IssuedAt
	}
	if now.Sub(last) < s.refreshInterval() {
		return nil
	}

	session.RefreshedAt = now
	return s.put(ctx, token, session)
}

// refreshInterval is a tenth of the lifetime and at most a minute, so a
// short-lived session still slides often enough.
func (s *SessionStore) refreshInterval() time.Duration {
	interval := s.ttl / 10
	if interval > maxRefreshInterval {
		interval = maxRefreshInterval
	}
	return interval
}

// Delete ends one session. An unknown token is not an error.
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.cache.Delete(ctx, sessionKey(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

func (s *SessionStore) TTL() time.Duration { return s.ttl }

// CookieLifetime is the shorter of the idle timeout and the maximum
// lifetime, so the browser stops sending the token no later than the server
// stops accepting it.
func (s *SessionStore) CookieLifetime() time.Duration { return min(s.ttl, s.maxLifetime) }

// sessionKey derives the cache key from the token's digest, so a leaked
// cache dump cannot be replayed.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return sessionKeyPrefix + hex.EncodeToString(sum[:])
}

// normalizeLogin lower-cases a login for lookups and rate-limit keys, as the
// unique index on lower(login) does, so case variants share a counter.
func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}
