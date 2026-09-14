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

// DefaultAccountCacheTTL is how long the middleware may decide on a copy of
// an account rather than the row, when the deployment does not state
// otherwise. See AccountCache for what it bounds.
const DefaultAccountCacheTTL = 5 * time.Second

const (
	// accountKeyPrefix namespaces cached accounts in the shared cache.
	accountKeyPrefix = "acct:"
	// accountGenerationKeyPrefix namespaces each account's cache generation.
	accountGenerationKeyPrefix = "acctgen:"

	// accountGenerationLifetime is how long a replaced generation is kept.
	// When it lapses the account reads the initial generation again, so every
	// entry written under that one has to be gone by then: an entry lives for
	// the cache's TTL, seconds, and is written by a request that read its
	// generation moments before. An hour is margin enough by orders of
	// magnitude, and costs one short key per account changed in that hour.
	accountGenerationLifetime = time.Hour

	// initialAccountGeneration is the generation of an account nothing has
	// been forgotten for yet, or whose last replacement has lapsed.
	initialAccountGeneration = "0"

	// forgetTimeout bounds the writes of one Forget. It runs after the
	// caller's change has committed, on a context detached from the request,
	// so nothing else would.
	forgetTimeout = 5 * time.Second
)

// AccountCache keeps, for a few seconds, the part of an account the
// authentication middleware decides on: status, session generation, the
// one-time-password flag and the permissions the account's roles grant.
// Without it every authenticated request reads the account with its roles
// and permissions from the database; with it the steady state is a read from
// the cache.
//
// # Invalidation
//
// Entries are keyed by the account and by a per-account cache generation, a
// random value kept in the same cache. Forget — which package users calls
// once a change to status, roles, password or sessions has committed —
// replaces the generation, and an entry under the old one is never read
// again. That is also what closes the race a plain delete loses: a request
// that read the account before the change and caches it after the Forget
// writes under the generation it read first, which nobody asks for any more.
// So when Forget reaches the cache, a blocked or deleted account is refused on
// its very next request.
//
// # The bound
//
// Where Forget cannot do that, the entry's lifetime (the TTL, 5 s by default)
// is the bound: a change is honoured at most one TTL after it commits. That
// covers a Forget that failed to write, a change made outside package users
// (SQL by hand; a migration that edits a role's permission set, which nothing
// in the running service can do), and an in-process cache shared by several
// instances, which is not a supported arrangement for sessions either. With
// Redis every instance reads the same generation, so a Forget made by one is
// seen by all.
//
// # Failure
//
// A cache that cannot be read is a miss: the middleware reads the database,
// exactly as it did before this cache existed, and nothing is written under a
// generation that could not be read. A garbled entry, or one naming another
// account, is a miss too. And a cached copy only ever lets a request through:
// when it would refuse one, the middleware asks the database before refusing,
// so a copy gone stale in the other direction — an account unblocked, a new
// session after a password change — never locks anybody out.
type AccountCache struct {
	cache cache.Cache
	ttl   time.Duration
	log   *slog.Logger
}

// NewAccountCache returns a cache whose entries live for ttl. A non-positive
// ttl keeps DefaultAccountCacheTTL.
func NewAccountCache(c cache.Cache, ttl time.Duration, log *slog.Logger) *AccountCache {
	if ttl <= 0 {
		ttl = DefaultAccountCacheTTL
	}
	return &AccountCache{cache: c, ttl: ttl, log: log}
}

// The account cache is what package users tells about changes.
var _ users.AccessCache = (*AccountCache)(nil)

// cachedAccount is what an entry holds: only what Authenticate decides on and
// puts into the identity. No digest, no contact details.
type cachedAccount struct {
	UserID             uuid.UUID `json:"user_id"`
	Login              string    `json:"login"`
	Status             string    `json:"status"`
	SessionGeneration  int64     `json:"session_generation"`
	MustChangePassword bool      `json:"must_change_password"`
	Permissions        []string  `json:"permissions"`
}

// accountSlot is where a lookup that missed may store what the database says.
// It carries the generation read before the database was, which is what makes
// a store that lands after a Forget harmless. The zero slot stores nothing.
type accountSlot struct{ key string }

// lookup returns the cached account and the slot to store a fresh copy in.
//
// Every failure is a miss; one that leaves the generation unknown also leaves
// the slot empty, so nothing is written for this request.
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
		// Unreadable, or somebody else's: either way not this account. The
		// slot stays, so the copy read from the database replaces it.
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

// generation reads the account's current cache generation.
func (a *AccountCache) generation(ctx context.Context, id uuid.UUID) (string, error) {
	raw, found, err := a.cache.Get(ctx, accountGenerationKey(id))
	if err != nil {
		return "", err
	}
	if !found {
		return initialAccountGeneration, nil
	}
	// Only a value Forget could have written is used as part of a key.
	if len(raw) != 32 {
		return "", fmt.Errorf("account cache generation of %d bytes", len(raw))
	}
	if _, err := hex.DecodeString(string(raw)); err != nil {
		return "", fmt.Errorf("account cache generation is not hex: %w", err)
	}
	return string(raw), nil
}

// store writes the account read from the database into the slot a lookup
// returned. It is called only after a miss, so a request served from the
// cache writes nothing.
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

// Forget makes every cached copy of the named accounts unreachable, by giving
// each a new cache generation. It implements users.AccessCache.
//
// The generation is 128 random bits, so it never repeats a value an old
// entry was written under. A failure is logged at error level and not
// returned: the change it follows has already committed, and the copy it
// could not drop still expires within the cache's TTL.
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

// accountGenerationKey names the cache generation of one account.
func accountGenerationKey(id uuid.UUID) string { return accountGenerationKeyPrefix + id.String() }
