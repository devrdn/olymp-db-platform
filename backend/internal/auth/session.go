// Package auth answers "who is this?": password-backed sign-in, server-side
// sessions, the cookie that carries them, brute-force throttling, and the
// middleware that turns a session into an identity for the rest of the API.
//
// It deliberately does not answer "may they do this?" — that is package rbac —
// nor does it own accounts, which belong to package users. Password hashing is
// a primitive in platform/password, kept separate so authentication and account
// management do not have to depend on each other.
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

// Sessions are server-side rather than self-contained tokens. A JWT cannot be
// withdrawn before it expires; a disqualified participant or a compromised
// account has to lose access immediately, which means the server has to hold
// the state.
const (
	// tokenBytes is the entropy handed to the client. 256 bits is far beyond
	// guessable and keeps the cookie short.
	tokenBytes = 32

	// sessionKeyPrefix namespaces session records inside the shared cache.
	sessionKeyPrefix = "sess:"
)

// ErrSessionNotFound reports a token that is unknown, expired or withdrawn.
// All three are the same thing to a caller: not authenticated.
var ErrSessionNotFound = errors.New("session not found")

// UserStore is the slice of the account repository that authentication needs:
// finding an account, and the two writes a successful sign-in performs.
//
// Declaring it here rather than importing users.Repository wholesale keeps the
// coupling to what is actually used — authentication has no business seeing
// Create, List or ReplaceRoles, and a test double only has to provide four
// methods. The production repository satisfies it without knowing it exists.
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
	// Generation pins the session to a point in the account's life. Blocking
	// an account or changing its password advances the counter, which retires
	// every session issued before it — including ones on other devices.
	Generation int64
}

// Session is a stored authentication record.
type Session struct {
	UserID     uuid.UUID `json:"user_id"`
	Login      string    `json:"login"`
	Generation int64     `json:"generation"`
	IssuedAt   time.Time `json:"issued_at"`
	// RefreshedAt is when the lifetime was last rewritten. Touch reads it to
	// decide whether the store needs another write at all; a record from
	// before the field existed leaves it zero and is treated as due.
	RefreshedAt time.Time `json:"refreshed_at,omitzero"`
}

// maxRefreshInterval caps how long an active session goes without its
// lifetime being rewritten. The sliding window is the product's promise; how
// often it slides is an implementation cost, and once a minute is
// indistinguishable from every request to anybody working through a contest.
const maxRefreshInterval = time.Minute

// DefaultMaxSessionLifetime is how long a session may exist from sign-in,
// however actively it is used, when the deployment does not state otherwise.
// A working day: longer than any contest, short enough that a copied cookie
// does not stay useful for days just because somebody keeps it warm.
const DefaultMaxSessionLifetime = 12 * time.Hour

// SessionStore keeps sessions in the shared cache.
//
// With Redis every replica sees the same sessions. On the in-process fallback
// they are per-instance, which is one of the reasons that mode is documented
// as single-instance only.
//
// A session ends at whichever comes first of two limits. The idle timeout
// (ttl) slides with activity, so somebody working through a contest is not
// signed out mid-answer. The maximum lifetime counts from sign-in and nothing
// extends it: without it, a session kept in use — by its owner or by whoever
// copied its cookie off a shared machine — never ends at all.
type SessionStore struct {
	cache       cache.Cache
	ttl         time.Duration
	maxLifetime time.Duration
}

// NewSessionStore returns a store whose sessions live for ttl, extended on
// activity by Refresh, and never longer than DefaultMaxSessionLifetime from
// sign-in.
func NewSessionStore(c cache.Cache, ttl time.Duration) *SessionStore {
	return &SessionStore{cache: c, ttl: ttl, maxLifetime: DefaultMaxSessionLifetime}
}

// WithMaxLifetime sets how long a session may exist from sign-in, however
// actively it is used. A non-positive value keeps the default. It is meant
// for the composition root, before the store is shared.
func (s *SessionStore) WithMaxLifetime(d time.Duration) *SessionStore {
	if d > 0 {
		s.maxLifetime = d
	}
	return s
}

// Create issues a session and returns its token. The token is the only copy
// the client ever sees; the store keeps a digest of it.
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

// put writes a session under its token for a full idle timeout, or for what
// is left of its maximum lifetime when that is shorter — so the store drops a
// session at its end even if nobody asks about it again.
func (s *SessionStore) put(ctx context.Context, token string, session Session) error {
	ttl := min(s.ttl, s.remaining(session, time.Now()))
	if ttl <= 0 {
		// Already past its end: there is nothing to extend, and writing it
		// would only resurrect a record Get is about to refuse.
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

// Get resolves a token to its session.
func (s *SessionStore) Get(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionNotFound
	}

	raw, found, err := s.cache.Get(ctx, sessionKey(token))
	if err != nil {
		// Fail closed: an unreadable session store means "not authenticated",
		// never "authenticated".
		return Session{}, fmt.Errorf("read session: %w", err)
	}
	if !found {
		return Session{}, ErrSessionNotFound
	}

	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}

	// Past its maximum lifetime the session is over, however recently it was
	// used. The record is removed so it stops occupying the store; if that
	// write fails, the refusal stands and the record's own expiry, which put
	// never set past this point, removes it anyway. A record without an issue
	// time is refused too: its age cannot be known, so it cannot be shown to
	// be within the limit.
	if s.remaining(session, time.Now()) <= 0 {
		_ = s.cache.Delete(ctx, sessionKey(token))
		return Session{}, ErrSessionNotFound
	}

	return session, nil
}

// remaining is how long the session has left before its maximum lifetime.
func (s *SessionStore) remaining(session Session, now time.Time) time.Duration {
	if session.IssuedAt.IsZero() {
		return 0
	}
	return session.IssuedAt.Add(s.maxLifetime).Sub(now)
}

// Refresh extends an active session by a full lifetime, unconditionally.
func (s *SessionStore) Refresh(ctx context.Context, token string) error {
	session, err := s.Get(ctx, token)
	if err != nil {
		return err
	}
	session.RefreshedAt = time.Now().UTC()
	return s.put(ctx, token, session)
}

// Touch extends a session the caller has already loaded, but only when the
// last extension is old enough to matter.
//
// The middleware authenticates every request and used to rewrite the session
// on each one: a read to authenticate, a second read inside Refresh, then a
// write — three round trips to the cache per request, two of them to move an
// expiry by a few seconds. Working from the record already in hand and
// skipping writes inside the refresh interval leaves one read per request for
// the common case. The cost is that the idle timeout is honoured to within
// one interval rather than exactly, which nobody can observe.
func (s *SessionStore) Touch(ctx context.Context, token string, session Session) error {
	now := time.Now().UTC()

	last := session.RefreshedAt
	if last.IsZero() {
		// Written before the field existed, or by a Create that predates it:
		// the issue time is the last moment the lifetime is known to have
		// been set.
		last = session.IssuedAt
	}
	if now.Sub(last) < s.refreshInterval() {
		return nil
	}

	session.RefreshedAt = now
	return s.put(ctx, token, session)
}

// refreshInterval is how long a session may go between lifetime rewrites: a
// tenth of the lifetime, and never more than a minute, so a short-lived
// session still slides often enough to stay alive under steady use.
func (s *SessionStore) refreshInterval() time.Duration {
	interval := s.ttl / 10
	if interval > maxRefreshInterval {
		interval = maxRefreshInterval
	}
	return interval
}

// Delete ends one session. An unknown token is not an error: logging out twice
// or with a stale cookie is a normal thing for a browser to do.
func (s *SessionStore) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.cache.Delete(ctx, sessionKey(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// TTL reports the configured session lifetime, used to set the cookie expiry.
func (s *SessionStore) TTL() time.Duration { return s.ttl }

// sessionKey derives the cache key from the token.
//
// The token itself is never stored. Anyone who reads the cache gets digests,
// which are useless for authenticating, so a leaked dump cannot be replayed as
// a live session.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return sessionKeyPrefix + hex.EncodeToString(sum[:])
}

// normalizeLogin lower-cases a login for lookups and rate-limit keys. The
// unique index is on lower(login), so authentication has to agree with it —
// otherwise "Ivanov" and "ivanov" would get separate throttle counters.
func normalizeLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}
