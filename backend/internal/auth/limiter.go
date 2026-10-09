package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
)

const limiterKeyPrefix = "rl:"

// Limiter counts attempts per subject in a fixed window. A sliding window
// would never reset under sustained guessing, locking the owner out.
type Limiter struct {
	cache cache.Cache
}

func NewLimiter(c cache.Cache) *Limiter {
	return &Limiter{cache: c}
}

// Allow records an attempt and reports whether it stays within the limit. A
// cache failure is (false, err): without the counter, refusing is safe.
func (l *Limiter) Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error) {
	count, err := l.cache.Incr(ctx, limiterKeyPrefix+subject, window)
	if err != nil {
		return false, fmt.Errorf("rate limit %s: %w", subject, err)
	}
	return count <= int64(limit), nil
}

const generationKeyPrefix = "rlgen:"

// Generation returns a family's current generation, which callers fold into
// every subject so NewGeneration can abandon all their counters at once. It is
// "0" when none is set. Callers refuse on an error, as with Allow.
func (l *Limiter) Generation(ctx context.Context, family string) (string, error) {
	raw, found, err := l.cache.Get(ctx, generationKeyPrefix+family)
	if err != nil {
		return "", fmt.Errorf("rate limit generation %s: %w", family, err)
	}
	if !found {
		return "0", nil
	}
	return string(raw), nil
}

// NewGeneration replaces a family's generation, abandoning every counter
// under the old one. The value is 128 random bits, so it never revives old
// counters. It lives for twice the family's longest window, so counters made
// under "0" have expired before "0" returns, with margin against timing.
func (l *Limiter) NewGeneration(ctx context.Context, family string, window time.Duration) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("new rate limit generation %s: %w", family, err)
	}
	value := hex.EncodeToString(raw)
	if err := l.cache.Set(ctx, generationKeyPrefix+family, []byte(value), 2*window); err != nil {
		return fmt.Errorf("new rate limit generation %s: %w", family, err)
	}
	return nil
}

// Reset clears a subject's counter after a successful attempt.
func (l *Limiter) Reset(ctx context.Context, subject string) error {
	if err := l.cache.Delete(ctx, limiterKeyPrefix+subject); err != nil {
		return fmt.Errorf("reset rate limit %s: %w", subject, err)
	}
	return nil
}
