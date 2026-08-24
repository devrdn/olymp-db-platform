package storage

import (
	"strings"
	"testing"
	"time"
)

func TestPoolConfigAppliesServiceDefaults(t *testing.T) {
	cfg, err := PoolConfig("postgres://app:secret@localhost:5432/core")
	if err != nil {
		t.Fatalf("PoolConfig() returned error: %v", err)
	}

	if cfg.MaxConns != defaultMaxConns {
		t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, defaultMaxConns)
	}
	if cfg.MaxConnLifetime != defaultMaxConnLifetime {
		t.Errorf("MaxConnLifetime = %v, want %v", cfg.MaxConnLifetime, defaultMaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != defaultMaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, defaultMaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod <= 0 {
		t.Errorf("HealthCheckPeriod = %v, want a positive interval", cfg.HealthCheckPeriod)
	}
}

func TestPoolConfigKeepsDSNSettings(t *testing.T) {
	cfg, err := PoolConfig("postgres://app:secret@db.internal:5433/core?pool_max_conns=7")
	if err != nil {
		t.Fatalf("PoolConfig() returned error: %v", err)
	}

	// An explicit pool size in the DSN must win over the service default, so
	// deployments can tune the pool without a code change.
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7 from the DSN", cfg.MaxConns)
	}
	if cfg.ConnConfig.Host != "db.internal" {
		t.Errorf("Host = %q, want db.internal", cfg.ConnConfig.Host)
	}
	if cfg.ConnConfig.Port != 5433 {
		t.Errorf("Port = %d, want 5433", cfg.ConnConfig.Port)
	}
}

func TestPoolConfigRejectsMalformedDSN(t *testing.T) {
	_, err := PoolConfig("://not a dsn")

	if err == nil {
		t.Fatal("PoolConfig() succeeded, want error for malformed DSN")
	}
}

func TestPoolConfigErrorDoesNotLeakCredentials(t *testing.T) {
	// Startup errors reach the logs; a DSN password must not travel with them.
	_, err := PoolConfig("postgres://app:sup3rs3cret@localhost:99999/core")

	if err == nil {
		t.Fatal("PoolConfig() succeeded, want error for invalid port")
	}
	if strings.Contains(err.Error(), "sup3rs3cret") {
		t.Errorf("error leaks the password: %v", err)
	}
}

func TestPoolConfigSetsStatementTimeoutGuard(t *testing.T) {
	cfg, err := PoolConfig("postgres://app:secret@localhost:5432/core")
	if err != nil {
		t.Fatalf("PoolConfig() returned error: %v", err)
	}

	// A runtime parameter caps any single statement, so one slow query cannot
	// pin a connection for the lifetime of the pool.
	got := cfg.ConnConfig.RuntimeParams["statement_timeout"]
	if got == "" {
		t.Fatal("statement_timeout is not set on core connections")
	}
	if d, err := time.ParseDuration(got + "ms"); err != nil || d <= 0 {
		t.Errorf("statement_timeout = %q, want a positive millisecond value", got)
	}
}
