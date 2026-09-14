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
	// defaultMaxConns is what a deployment gets unless it sets
	// CORE_DB_POOL_MAX (config.Config.CoreDBPoolMax) or embeds
	// pool_max_conns in the DSN itself. Ten was sized for a trickle of admin
	// traffic; a live olympiad draws on this pool far more widely — a
	// roster's console pre-checks and submissions, SSE resyncs, the
	// leaderboard's own reads, staff dashboards, and a CSV export that pins
	// one connection for up to a minute apiece (participant_handler.go's
	// exportDeadline), with no cap on how many run at once. 25 leaves room
	// for several such exports alongside the steady stream of short
	// request-scoped queries everything else makes, without asking Postgres
	// for more than one API instance will ever hold open (see pg-core's
	// max_connections in deploy/docker-compose.yml for the arithmetic on the
	// other side).
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

// PoolConfig parses the DSN and applies the service pool defaults. Settings
// present in the DSN are preserved, so a deployment can tune the pool without
// a code change.
//
// Errors deliberately omit the DSN: it carries the database password and
// startup errors end up in the logs.
// dsnSetsPoolMaxConns reports whether the connection string chooses the pool
// size itself.
//
// It has to be asked, because pgxpool never leaves MaxConns unset: parsing
// fills it with max(4, NumCPU). A "was it left at zero" check therefore never
// fires, which silently handed the pool size to whatever machine happened to
// run the process.
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

func PoolConfig(dsn string) (*pgxpool.Config, error) {
	return poolConfig(dsn, coreStatementTimeout, 0)
}

// PoolConfigWithMaxConns is PoolConfig, taking the pool size a deployment
// configured (config.Config.CoreDBPoolMax, from CORE_DB_POOL_MAX) rather than
// the service default. maxConns <= 0 means the deployment left it unset and
// keeps PoolConfig's own default; either way a pool_max_conns the DSN already
// sets still wins, so the two ways of tuning the pool cannot disagree with
// each other.
func PoolConfigWithMaxConns(dsn string, maxConns int32) (*pgxpool.Config, error) {
	return poolConfig(dsn, coreStatementTimeout, maxConns)
}

// MaintenancePoolConfig is PoolConfig with a statement timeout sized for
// administrative work rather than for request queries.
//
// The core timeout of ten seconds is right for the API and wrong for a pool
// whose statements are CREATE DATABASE … TEMPLATE and DROP DATABASE: copying
// a large game template takes as long as the disk takes, and a ten-second cap
// would fail provisioning exactly for the contests big enough to need it —
// silently, at the first tick that tried.
func MaintenancePoolConfig(dsn string, statementTimeout time.Duration) (*pgxpool.Config, error) {
	return poolConfig(dsn, statementTimeout, 0)
}

func poolConfig(dsn string, statementTimeout time.Duration, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse core database DSN: invalid connection string")
	}

	// Not "if it is zero": pgxpool has already put max(4, NumCPU) there. The
	// question is whether the deployment asked for a size, and only if it did
	// not does a configured override or the service default apply.
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

// NewPoolWithMaxConns is NewPool, sized by PoolConfigWithMaxConns instead of
// PoolConfig. The Core API is the one caller that passes a configured value
// (config.Config.CoreDBPoolMax); every other caller — the one-off tools that
// briefly open their own pool against this database — keeps asking for
// NewPool's own default, which is enough for a job that runs alone.
func NewPoolWithMaxConns(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := PoolConfigWithMaxConns(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	return openPool(ctx, cfg)
}

// NewMaintenancePool opens a pool for administrative statements — the game
// cluster's provisioner — whose statement timeout is the one given rather
// than the core API's.
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
