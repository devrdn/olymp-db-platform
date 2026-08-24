package storage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool defaults sized for the Core API: short-lived request queries only.
// The game cluster is served by the Query Runner with its own settings.
const (
	defaultMaxConns        = int32(10)
	defaultMinConns        = int32(2)
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 30 * time.Minute
	defaultHealthCheck     = time.Minute

	// coreStatementTimeout caps a single statement on the core database. No
	// legitimate API query is slow; a query that is must not hold its
	// connection while participants are waiting.
	coreStatementTimeout = 10 * time.Second
)

// PoolConfig parses the DSN and applies the service pool defaults. Settings
// present in the DSN are preserved, so a deployment can tune the pool without
// a code change.
//
// Errors deliberately omit the DSN: it carries the database password and
// startup errors end up in the logs.
func PoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core database DSN: invalid connection string")
	}

	if cfg.MaxConns == 0 {
		cfg.MaxConns = defaultMaxConns
	}
	if cfg.MinConns == 0 {
		cfg.MinConns = defaultMinConns
	}
	if cfg.MaxConnLifetime == 0 {
		cfg.MaxConnLifetime = defaultMaxConnLifetime
	}
	if cfg.MaxConnIdleTime == 0 {
		cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	}
	if cfg.HealthCheckPeriod == 0 {
		cfg.HealthCheckPeriod = defaultHealthCheck
	}

	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
		ms := strconv.FormatInt(coreStatementTimeout.Milliseconds(), 10)
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = ms
	}

	return cfg, nil
}

// NewPool opens the core database pool and verifies it is reachable, so a
// misconfigured deployment fails at startup rather than on the first request.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := PoolConfig(dsn)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open core database pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("core database is unreachable: %w", err)
	}

	return pool, nil
}
