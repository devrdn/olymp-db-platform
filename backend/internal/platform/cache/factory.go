package cache

import (
	"context"
	"log/slog"
)

// Modes reported by Mode, surfaced in the readiness response.
const (
	ModeRedis  = "redis"
	ModeMemory = "memory"
)

// New builds the cache backend for the given address. An empty address
// selects the in-process store. A non-empty one that does not answer is an
// error, not a reason to fall back: a silent fallback would hide a broken
// deployment and break session sharing between replicas.
func New(ctx context.Context, addr string, log *slog.Logger) (Cache, error) {
	if addr == "" {
		log.Warn("no Redis address configured, using the in-process cache",
			"consequence", "sessions and rate limits are not shared between replicas and are lost on restart",
			"supported_for", "single-instance deployments only",
		)
		return NewMemory(defaultCapacity), nil
	}

	client, err := NewRedis(ctx, addr)
	if err != nil {
		return nil, err
	}

	log.Info("cache backend ready", "backend", ModeRedis)
	return client, nil
}

func Mode(c Cache) string {
	if _, ok := c.(*Redis); ok {
		return ModeRedis
	}
	return ModeMemory
}
