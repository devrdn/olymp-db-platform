package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// generationKeyPrefix namespaces generation records in the shared cache.
const generationKeyPrefix = "rlgen:"

// Generation returns the current generation of a family of subjects: a token
// the caller folds into every subject of the family, so NewGeneration can
// abandon all of their counters at once without knowing what they are. It is
// "0" when no generation was ever set, or the last one has lapsed.
//
// A cache failure is an error, and callers refuse on it exactly as Allow
// does: a generation that cannot be read is a counter that cannot be found.
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
// keyed under the old one.
//
// The value is 128 random bits in hex, so it does not repeat — a repeated
// value would bring counters abandoned by an earlier generation back, and a
// clock is not fine-grained enough to promise that for two unlocks in a row.
// It lives for window, the longest window any counter of the family uses: by
// the time it lapses and the family reads "0" again, every counter created
// under "0" before the first replacement has expired with its own window.
func (l *Limiter) NewGeneration(ctx context.Context, family string, window time.Duration) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("new rate limit generation %s: %w", family, err)
	}
	value := hex.EncodeToString(raw)
	if err := l.cache.Set(ctx, generationKeyPrefix+family, []byte(value), window); err != nil {
		return fmt.Errorf("new rate limit generation %s: %w", family, err)
	}
	return nil
}

// Reset clears a subject's counter, which a successful attempt does so a user
// who mistypes and then succeeds is not still near the limit.
func (l *Limiter) Reset(ctx context.Context, subject string) error {
	if err := l.cache.Delete(ctx, limiterKeyPrefix+subject); err != nil {
		return fmt.Errorf("reset rate limit %s: %w", subject, err)
	}
	return nil
}
