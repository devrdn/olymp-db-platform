package auth

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
)

func newTestLimiter(t *testing.T) *Limiter {
	t.Helper()
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	return NewLimiter(c)
}

func TestLimiterAllowsAttemptsUpToTheLimit(t *testing.T) {
	limiter := newTestLimiter(t)
	ctx := context.Background()

	for attempt := 1; attempt <= 3; attempt++ {
		allowed, err := limiter.Allow(ctx, "login:ivanov", 3, time.Minute)
		if err != nil {
			t.Fatalf("attempt %d returned error: %v", attempt, err)
		}
		if !allowed {
			t.Fatalf("attempt %d was blocked, but the limit is 3", attempt)
		}
	}
}

func TestLimiterBlocksBeyondTheLimit(t *testing.T) {
	limiter := newTestLimiter(t)
	ctx := context.Background()
	for range 3 {
		_, _ = limiter.Allow(ctx, "login:ivanov", 3, time.Minute)
	}

	allowed, err := limiter.Allow(ctx, "login:ivanov", 3, time.Minute)

	if err != nil {
		t.Fatalf("Allow() returned error: %v", err)
	}
	if allowed {
		t.Error("the fourth attempt was allowed although the limit is 3")
	}
}

func TestLimiterCountsSubjectsSeparately(t *testing.T) {
	// One account being hammered must not lock out everybody else.
	limiter := newTestLimiter(t)
	ctx := context.Background()
	for range 5 {
		_, _ = limiter.Allow(ctx, "login:victim", 3, time.Minute)
	}

	allowed, _ := limiter.Allow(ctx, "login:bystander", 3, time.Minute)

	if !allowed {
		t.Error("an unrelated subject was blocked by another subject's attempts")
	}
}

func TestLimiterForgetsAfterTheWindow(t *testing.T) {
	limiter := newTestLimiter(t)
	ctx := context.Background()
	for range 3 {
		_, _ = limiter.Allow(ctx, "login:ivanov", 3, 20*time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)

	allowed, _ := limiter.Allow(ctx, "login:ivanov", 3, 20*time.Millisecond)
	if !allowed {
		t.Error("the window expired but the subject is still blocked")
	}
}

func TestLimiterResetClearsTheCounter(t *testing.T) {
	// A successful login clears the failure count, so a user who mistypes twice
	// and then succeeds starts fresh.
	limiter := newTestLimiter(t)
	ctx := context.Background()
	for range 3 {
		_, _ = limiter.Allow(ctx, "login:ivanov", 3, time.Minute)
	}

	if err := limiter.Reset(ctx, "login:ivanov"); err != nil {
		t.Fatalf("Reset() returned error: %v", err)
	}

	allowed, _ := limiter.Allow(ctx, "login:ivanov", 3, time.Minute)
	if !allowed {
		t.Error("the subject is still blocked after Reset")
	}
}

func TestLimiterFailsClosedWhenTheCacheIsBroken(t *testing.T) {
	// If the counter cannot be read, the brute-force protection is not working;
	// refusing the attempt is the safe reading.
	limiter := NewLimiter(brokenCache{})

	allowed, err := limiter.Allow(context.Background(), "login:ivanov", 3, time.Minute)

	if err == nil {
		t.Fatal("Allow() hid a cache failure")
	}
	if allowed {
		t.Error("Allow() permitted the attempt although the counter is unreadable")
	}
}

// brokenCache fails every operation.
type brokenCache struct{ cache.Cache }

func (brokenCache) Incr(context.Context, string, time.Duration) (int64, error) {
	return 0, context.DeadlineExceeded
}
