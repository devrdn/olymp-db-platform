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

// Redis is the shared cache backend, the only one correct with more than one
// API replica.
type Redis struct {
	client *redis.Client
}

// NewRedis opens a client and verifies the server answers, so a wrong address
// is reported at startup.
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

// How long one cache call may take before it is an error. The caller's
// context may carry no deadline, so the bound is the client's; the library
// defaults (three retries of three seconds) would let one slow server hold
// every request for a dozen seconds.
const (
	redisDialTimeout  = 2 * time.Second
	redisIOTimeout    = time.Second
	redisMaxRetries   = 1
	redisPoolWaitTime = 2 * time.Second
)

// Options accepts both the URL form ("redis://host:6379/0") and the bare
// "host:port" form. Timeouts the address names are kept; the bounds above fill
// in the rest.
func Options(addr string) (*redis.Options, error) {
	var opts *redis.Options
	if strings.Contains(addr, "://") {
		parsed, err := redis.ParseURL(addr)
		if err != nil {
			return nil, fmt.Errorf("parse Redis URL: %w", err)
		}
		opts = parsed
	} else {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return nil, fmt.Errorf("parse Redis address: %w", err)
		}
		opts = &redis.Options{Addr: addr}
	}
	bound(opts)
	return opts, nil
}

func bound(opts *redis.Options) {
	if opts.DialTimeout == 0 {
		opts.DialTimeout = redisDialTimeout
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = redisIOTimeout
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = redisIOTimeout
	}
	if opts.PoolTimeout == 0 {
		opts.PoolTimeout = redisPoolWaitTime
	}
	// Zero means the library's three retries, each another second spent on a
	// server that is not answering.
	if opts.MaxRetries == 0 {
		opts.MaxRetries = redisMaxRetries
	}
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	value, err := r.client.Get(ctx, key).Result()
	return classifyGet(value, err)
}

// classifyGet maps a Redis reply to the Cache contract: redis.Nil is a miss,
// not a failure.
func classifyGet(value string, err error) ([]byte, bool, error) {
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache get: %w", err)
	}
	return []byte(value), true, nil
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache set: %w", err)
	}
	return nil
}

func (r *Redis) Delete(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// Incr increments a counter and sets the TTL on first use. Both commands go
// in one pipeline, so a crash between them cannot leave a counter, and a rate
// limit, that never expires.
func (r *Redis) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("cache incr: %w", err)
	}
	return incr.Val(), nil
}

func (r *Redis) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *Redis) Close() error {
	return r.client.Close()
}
