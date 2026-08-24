// Package cache provides the key-value store used for sessions, rate-limit
// windows and hot reads, behind an interface with two implementations.
//
// Redis is the production backend. When no Redis address is configured the
// service falls back to an in-process store instead of refusing to start, so a
// single-node install (a lecture-hall laptop, a developer machine) needs one
// less moving part.
//
// The fallback is not equivalent, and the difference is not cosmetic: an
// in-process store is not shared between replicas, so sessions and rate limits
// would diverge per instance, and it is lost on restart. Selecting it is
// therefore a deliberate act (see New), never a silent downgrade.
package cache

import (
	"context"
	"time"
)

// Cache is the contract every backend implements.
//
// Get reports found=false for a missing or expired key. Callers must treat a
// returned error as a miss for read-through caching, but must fail closed for
// anything security-relevant: an unreadable session is "not authenticated",
// never "authenticated".
type Cache interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	// Incr increments a counter and returns its new value, setting the TTL on
	// first use. It is the rate-limiting primitive.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// Ping reports whether the backend is usable, for the readiness probe.
	Ping(ctx context.Context) error
	Close() error
}
