package cache

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is the shared cache backend. It is the only one that works correctly
// with more than one API replica.
type Redis struct {
	client *redis.Client
}

// NewRedis opens a client and verifies the server answers, so a wrong address
// is reported at startup rather than on the first request.
func NewRedis(ctx context.Context, addr string) (*Redis, error) {
	opts, err := Options(addr)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis is unreachable: %w", err)
	}

	return &Redis{client: client}, nil
}

// Options accepts both the URL form ("redis://host:6379/0") and the bare
// "host:port" form used in compose files.
func Options(addr string) (*redis.Options, error) {
	if strings.Contains(addr, "://") {
		opts, err := redis.ParseURL(addr)
		if err != nil {
			return nil, fmt.Errorf("parse Redis URL: %w", err)
		}
		return opts, nil
	}

	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("parse Redis address: %w", err)
	}
	return &redis.Options{Addr: addr}, nil
}

// Get returns the stored value, or found=false when the key is absent.
func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := r.client.Get(ctx, key).Result()
	return classifyGet(value, err)
}

// classifyGet maps a Redis reply to the Cache contract. redis.Nil is the
// server saying "no such key", which is a miss and not a failure.
func classifyGet(value string, err error) ([]byte, bool, error) {
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get: %w", err)
	}
	return []byte(value), true, nil
}

// Set stores a value for ttl.
func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

// Delete removes a key, whether or not it was present.
func (r *Redis) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// Incr increments a counter and sets the TTL on first use.
//
// Both commands go in one pipeline: two round trips would leave a window where
// a crash between them creates a counter that never expires, and a rate limit
// that never lifts.
func (r *Redis) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("cache incr: %w", err)
	}
	return incr.Val(), nil
}

// Ping reports whether the server answers.
func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (r *Redis) Close() error {
	return r.client.Close()
}
