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
}

// SessionStore keeps sessions in the shared cache.
//
// With Redis every replica sees the same sessions. On the in-process fallback
// they are per-instance, which is one of the reasons that mode is documented
// as single-instance only.
type SessionStore struct {
	cache cache.Cache
	ttl   time.Duration
}

// NewSessionStore returns a store whose sessions live for ttl, extended on
// activity by Refresh.
func NewSessionStore(c cache.Cache, ttl time.Duration) *SessionStore {
	return &SessionStore{cache: c, ttl: ttl}
}

// Create issues a session and returns its token. The token is the only copy
// the client ever sees; the store keeps a digest of it.
func (s *SessionStore) Create(ctx context.Context, p Principal) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	record, err := json.Marshal(Session{
		UserID:     p.UserID,
		Login:      p.Login,
		Generation: p.Generation,
		IssuedAt:   time.Now().UTC(),
	})
	if err != nil {
		return "", fmt.Errorf("encode session: %w", err)
	}

	if err := s.cache.Set(ctx, sessionKey(token), record, s.ttl); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}

	return token, nil
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

	return session, nil
}

// Refresh extends an active session by a full lifetime.
func (s *SessionStore) Refresh(ctx context.Context, token string) error {
	session, err := s.Get(ctx, token)
	if err != nil {
		return err
	}

	record, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}

	if err := s.cache.Set(ctx, sessionKey(token), record, s.ttl); err != nil {
		return fmt.Errorf("refresh session: %w", err)
	}
	return nil
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
