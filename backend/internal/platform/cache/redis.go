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

// How long one cache call may take before it is an error.
//
// A cache call sits inside an ordinary request — a session read, a rate-limit
// check — and the caller's context may carry no deadline of its own, so the
// bound has to be the client's. The library's defaults are three retries with
// a three-second read timeout apiece: one slow server then holds every
// request for a dozen seconds, and the requests queueing behind them are
// goroutines that do not leave. A cache that cannot answer within a second is
// not a cache; the caller treats the error the way it treats any other cache
// failure.
const (
	redisDialTimeout  = 2 * time.Second
	redisIOTimeout    = time.Second
	redisMaxRetries   = 1
	redisPoolWaitTime = 2 * time.Second
)

// Options accepts both the URL form ("redis://host:6379/0") and the bare
// "host:port" form used in compose files.
//
// Timeouts an address names are kept: an operator who writes read_timeout
// into the URL means it. The bounds above fill in everything it leaves unset,
// which is the ordinary case.
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

// bound fills in the timeouts the address did not name.
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
	// Zero means the library's own three. A retry of a call that has already
	// waited out its timeout is another second of the request spent on a
	// server that is not answering.
	if opts.MaxRetries == 0 {
		opts.MaxRetries = redisMaxRetries
	}
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
