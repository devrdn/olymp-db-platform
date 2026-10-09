package storage

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool defaults sized for the Core API: short-lived request queries only.
// The game cluster is served by the Query Runner with its own settings.
const (
	// defaultMaxConns applies unless CORE_DB_POOL_MAX or the DSN's
	// pool_max_conns sets the size. A live contest draws on this pool for
	// submissions, SSE resyncs and dashboards, and a CSV export can hold a
	// connection for up to a minute; 25 leaves room for several exports. Keep
	// it within pg-core's max_connections in deploy/docker-compose.yml.
	defaultMaxConns        = int32(25)
	defaultMinConns        = int32(2)
	defaultMaxConnLifetime = time.Hour
	defaultMaxConnIdleTime = 30 * time.Minute
	defaultHealthCheck     = time.Minute

	// coreStatementTimeout caps a single statement on the core database. No
	// legitimate API query is slow; a query that is must not hold its
	// connection while participants are waiting.
	coreStatementTimeout = 10 * time.Second
)

// dsnSetsPoolMaxConns reports whether the connection string sets the pool
// size. Checking MaxConns for zero does not work: pgxpool fills it with
// max(4, NumCPU) while parsing.
func dsnSetsPoolMaxConns(dsn string) bool {
	const key = "pool_max_conns"

	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		return u.Query().Has(key)
	}

	// Keyword/value form: "host=localhost pool_max_conns=25 dbname=core".
	for _, field := range strings.Fields(dsn) {
		if name, _, found := strings.Cut(field, "="); found && name == key {
			return true
		}
	}
	return false
}

// PoolConfig parses the DSN and applies the service pool defaults; settings
// present in the DSN win. Errors omit the DSN because it carries the password.
func PoolConfig(dsn string) (*pgxpool.Config, error) {
	return poolConfig(dsn, coreStatementTimeout, 0)
}

// PoolConfigWithMaxConns is PoolConfig with a configured pool size. maxConns
// <= 0 keeps the default, and pool_max_conns in the DSN still wins.
func PoolConfigWithMaxConns(dsn string, maxConns int32) (*pgxpool.Config, error) {
	return poolConfig(dsn, coreStatementTimeout, maxConns)
}

// MaintenancePoolConfig is PoolConfig with the given statement timeout. CREATE
// DATABASE ... TEMPLATE copies a whole template and can outlast the core
// ten-second cap (CLAUDE.md rule 15).
func MaintenancePoolConfig(dsn string, statementTimeout time.Duration) (*pgxpool.Config, error) {
	return poolConfig(dsn, statementTimeout, 0)
}

func poolConfig(dsn string, statementTimeout time.Duration, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core database DSN: invalid connection string")
	}

	if !dsnSetsPoolMaxConns(dsn) {
		if maxConns > 0 {
			cfg.MaxConns = maxConns
		} else {
			cfg.MaxConns = defaultMaxConns
		}
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
		ms := strconv.FormatInt(statementTimeout.Milliseconds(), 10)
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = ms
	}

	return cfg, nil
}

// NewPool opens the core database pool and verifies it is reachable, so a
// misconfigured deployment fails at startup rather than on the first request.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return NewPoolWithMaxConns(ctx, dsn, 0)
}

// NewPoolWithMaxConns is NewPool sized by PoolConfigWithMaxConns.
func NewPoolWithMaxConns(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := PoolConfigWithMaxConns(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	return openPool(ctx, cfg)
}

// NewMaintenancePool opens a pool for administrative statements, such as the
// game cluster's provisioner, with the given statement timeout.
func NewMaintenancePool(ctx context.Context, dsn string, statementTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := MaintenancePoolConfig(dsn, statementTimeout)
	if err != nil {
		return nil, err
	}
	return openPool(ctx, cfg)
}

func openPool(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
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
