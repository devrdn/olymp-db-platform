package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// DefaultAccountCacheTTL is how long the middleware may decide on a cached
// account (see AccountCache).
const DefaultAccountCacheTTL = 5 * time.Second

const (
	accountKeyPrefix           = "acct:"
	accountGenerationKeyPrefix = "acctgen:"

	// accountGenerationLifetime is how long a replaced generation is kept.
	// When it lapses the initial generation returns, so every entry under it
	// must be gone by then; entries live seconds, so an hour is ample.
	accountGenerationLifetime = time.Hour

	initialAccountGeneration = "0"

	// forgetTimeout bounds one Forget, which runs on a detached context.
	forgetTimeout = 5 * time.Second
)

// AccountCache keeps, for a few seconds, what the authentication middleware
// decides on: status, session generation, the one-time-password flag and
// permissions. The steady state is then a cache read, not a database query.
//
// Entries are keyed by account and a per-account random generation. Forget
// replaces the generation once a change commits, so an older entry is never
// read again; this also defeats the race a plain delete loses, where a
// request that read the old row caches it after the Forget.
//
// Otherwise the TTL bounds staleness: a failed Forget, a change made outside
// package users (SQL by hand), or several instances on in-process caches.
//
// An unreadable or garbled entry is a miss. A cached copy only ever admits a
// request; a refusal is always checked against the database, so a copy stale
// the other way never locks anybody out.
type AccountCache struct {
	cache cache.Cache
	ttl   time.Duration
	log   *slog.Logger
}

// NewAccountCache returns a cache whose entries live for ttl; non-positive
// keeps DefaultAccountCacheTTL.
func NewAccountCache(c cache.Cache, ttl time.Duration, log *slog.Logger) *AccountCache {
	if ttl <= 0 {
		ttl = DefaultAccountCacheTTL
	}
	return &AccountCache{cache: c, ttl: ttl, log: log}
}

var _ users.AccessCache = (*AccountCache)(nil)

// cachedAccount holds only what Authenticate needs: no digest, no contact
// details.
type cachedAccount struct {
	UserID             uuid.UUID `json:"user_id"`
	Login              string    `json:"login"`
	Status             string    `json:"status"`
	SessionGeneration  int64     `json:"session_generation"`
	MustChangePassword bool      `json:"must_change_password"`
	Permissions        []string  `json:"permissions"`
}

// accountSlot is where a missed lookup may store the database's answer. It
// carries the generation read before the database, so a store landing after a
// Forget is harmless. The zero slot stores nothing.
type accountSlot struct{ key string }

// lookup returns the cached account and the slot to store a fresh copy in.
// Every failure is a miss; with the generation unknown the slot is empty.
func (a *AccountCache) lookup(ctx context.Context, id uuid.UUID) (users.User, bool, accountSlot) {
	generation, err := a.generation(ctx, id)
	if err != nil {
		a.log.WarnContext(ctx, "could not read the account cache generation; reading the account instead",
			"user_id", id, "error", err)
		return users.User{}, false, accountSlot{}
	}
	slot := accountSlot{key: accountKeyPrefix + id.String() + ":" + generation}

	raw, found, err := a.cache.Get(ctx, slot.key)
	if err != nil {
		a.log.WarnContext(ctx, "could not read the cached account; reading the account instead",
			"user_id", id, "error", err)
		return users.User{}, false, accountSlot{}
	}
	if !found {
		return users.User{}, false, slot
	}

	var entry cachedAccount
	if err := json.Unmarshal(raw, &entry); err != nil || entry.UserID != id {
		// Unreadable or another account's: the fresh copy replaces it.
		return users.User{}, false, slot
	}
	return users.User{
		ID:                 entry.UserID,
		Login:              entry.Login,
		Status:             entry.Status,
		SessionGeneration:  entry.SessionGeneration,
		MustChangePassword: entry.MustChangePassword,
		Permissions:        entry.Permissions,
	}, true, slot
}

func (a *AccountCache) generation(ctx context.Context, id uuid.UUID) (string, error) {
	raw, found, err := a.cache.Get(ctx, accountGenerationKey(id))
	if err != nil {
		return "", err
	}
	if !found {
		return initialAccountGeneration, nil
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("account cache generation of %d bytes", len(raw))
	}
	if _, err := hex.DecodeString(string(raw)); err != nil {
		return "", fmt.Errorf("account cache generation is not hex: %w", err)
	}
	return string(raw), nil
}

// store writes the database's account into the slot, only after a miss.
func (a *AccountCache) store(ctx context.Context, slot accountSlot, account users.User) {
	if slot.key == "" {
		return
	}
	raw, err := json.Marshal(cachedAccount{
		UserID:             account.ID,
		Login:              account.Login,
		Status:             account.Status,
		SessionGeneration:  account.SessionGeneration,
		MustChangePassword: account.MustChangePassword,
		Permissions:        account.Permissions,
	})
	if err != nil {
		a.log.WarnContext(ctx, "could not encode the account for the cache", "user_id", account.ID, "error", err)
		return
	}
	if err := a.cache.Set(ctx, slot.key, raw, a.ttl); err != nil {
		a.log.WarnContext(ctx, "could not cache the account", "user_id", account.ID, "error", err)
	}
}

// Forget makes every cached copy of the named accounts unreachable by giving
// each a new random 128-bit generation. A failure is logged, not returned:
// the change has committed, and the copy expires within the TTL.
func (a *AccountCache) Forget(ctx context.Context, ids []uuid.UUID) {
	if a == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, forgetTimeout)
	defer cancel()

	for _, id := range ids {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			a.log.ErrorContext(ctx, "could not drop the cached account; the change applies once it expires",
				"user_id", id, "ttl", a.ttl, "error", err)
			continue
		}
		generation := []byte(hex.EncodeToString(raw))
		if err := a.cache.Set(ctx, accountGenerationKey(id), generation, accountGenerationLifetime); err != nil {
			a.log.ErrorContext(ctx, "could not drop the cached account; the change applies once it expires",
				"user_id", id, "ttl", a.ttl, "error", err)
		}
	}
}

func accountGenerationKey(id uuid.UUID) string { return accountGenerationKeyPrefix + id.String() }
