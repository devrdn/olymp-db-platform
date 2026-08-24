package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
)

// limiterKeyPrefix namespaces rate-limit counters in the shared cache.
const limiterKeyPrefix = "rl:"

// Limiter counts attempts per subject in a fixed window.
//
// The window is fixed rather than sliding on purpose: a sliding window that is
// extended by every attempt never resets under sustained load, which turns a
// brute-force attempt against one account into a denial of service against its
// owner.
type Limiter struct {
	cache cache.Cache
}

// NewLimiter returns a limiter backed by the shared cache.
func NewLimiter(c cache.Cache) *Limiter {
	return &Limiter{cache: c}
}

// Allow records an attempt and reports whether it stays within the limit.
//
// A cache failure is reported as (false, err): if the counter cannot be
// maintained, the protection is not in place, and refusing is the safe reading.
func (l *Limiter) Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error) {
	count, err := l.cache.Incr(ctx, limiterKeyPrefix+subject, window)
	if err != nil {
		return false, fmt.Errorf("rate limit %s: %w", subject, err)
	}
	return count <= int64(limit), nil
}

// Reset clears a subject's counter, which a successful attempt does so a user
// who mistypes and then succeeds is not still near the limit.
func (l *Limiter) Reset(ctx context.Context, subject string) error {
	if err := l.cache.Delete(ctx, limiterKeyPrefix+subject); err != nil {
		return fmt.Errorf("reset rate limit %s: %w", subject, err)
	}
	return nil
}
