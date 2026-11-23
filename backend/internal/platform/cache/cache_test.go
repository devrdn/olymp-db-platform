package cache

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// adapters is every backend the contract is checked against: the in-process
// store always, and Redis when CACHE_TEST_REDIS_ADDR names a throwaway server,
// for example
//
//	docker run -d --rm --name cache-test-redis -p 56379:6379 --tmpfs /data redis:7-alpine
//	CACHE_TEST_REDIS_ADDR=localhost:56379 go test ./internal/platform/cache/
func adapters(t *testing.T) map[string]Cache {
	t.Helper()
	all := map[string]Cache{"memory": NewMemory(100)}
	if addr := os.Getenv("CACHE_TEST_REDIS_ADDR"); addr != "" {
		r, err := NewRedis(t.Context(), addr)
		if err != nil {
			t.Fatalf("CACHE_TEST_REDIS_ADDR=%s: %v", addr, err)
		}
		all["redis"] = r
	} else {
		t.Log("CACHE_TEST_REDIS_ADDR is unset, so Redis is not checked")
	}
	for _, c := range all {
		t.Cleanup(func() { _ = c.Close() })
	}
	return all
}

// TTL is what a refused caller is told to wait, so it must mean the same on
// the backend a deployment uses as on the one the tests use (CLAUDE.md rule
// 10).
func TestTTLIsWhatIsLeftOfAKeysLifeOnEveryBackend(t *testing.T) {
	for name, c := range adapters(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			counter, value, missing := "test:ttl:"+uuid.NewString(), "test:ttl:"+uuid.NewString(), "test:ttl:"+uuid.NewString()
			t.Cleanup(func() {
				_ = c.Delete(ctx, counter)
				_ = c.Delete(ctx, value)
			})

			if _, err := c.Incr(ctx, counter, time.Minute); err != nil {
				t.Fatal(err)
			}
			first, err := c.TTL(ctx, counter)
			if err != nil || first <= 0 || first > time.Minute {
				t.Fatalf("a fresh counter's TTL = %s, %v, want at most its minute", first, err)
			}
			time.Sleep(20 * time.Millisecond)
			if _, err := c.Incr(ctx, counter, time.Minute); err != nil {
				t.Fatal(err)
			}
			if again, err := c.TTL(ctx, counter); err != nil || again <= 0 || again >= first {
				t.Errorf("after a second Incr TTL = %s, %v, want less than %s: the window is fixed", again, err, first)
			}

			if err := c.Set(ctx, value, []byte("x"), time.Hour); err != nil {
				t.Fatal(err)
			}
			if ttl, err := c.TTL(ctx, value); err != nil || ttl <= time.Hour-time.Minute || ttl > time.Hour {
				t.Errorf("a value's TTL = %s, %v, want about its hour", ttl, err)
			}
			if err := c.Delete(ctx, value); err != nil {
				t.Fatal(err)
			}

			for _, key := range []string{missing, value} {
				if ttl, err := c.TTL(ctx, key); ttl != 0 || err != nil {
					t.Errorf("TTL of a key that is not there = %s, %v, want 0", ttl, err)
				}
			}
		})
	}
}
