// Package cache provides the key-value store for sessions, rate-limit windows
// and hot reads. Redis is the production backend; without a configured
// address the service uses an in-process store, which suits a single node
// only: it is not shared between replicas and is lost on restart.
//
// It does not decide what is cached or for how long; callers do.
package cache

import (
	"context"
	"time"
)

// Cache is the contract every backend implements.
//
// Get reports found=false for a missing or expired key. Callers treat an error
// as a miss for read-through caching, but must fail closed for anything
// security-relevant: an unreadable session is "not authenticated".
type Cache interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	// Incr increments a counter and returns its new value, setting the TTL on
	// first use.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}
