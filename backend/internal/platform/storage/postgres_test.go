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

func TestDSNSetsPoolMaxConns(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want bool
	}{
		{"url form without the setting", "postgres://app:secret@localhost:5432/core", false},
		{"url form with the setting", "postgres://app:secret@localhost:5432/core?pool_max_conns=25", true},
		{"keyword form without the setting", "host=localhost user=app dbname=core", false},
		{"keyword form with the setting", "host=localhost pool_max_conns=25 dbname=core", true},
		{"a database merely named after the setting", "postgres://app@localhost/pool_max_conns", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dsnSetsPoolMaxConns(c.dsn); got != c.want {
				t.Errorf("dsnSetsPoolMaxConns(%q) = %v, want %v", c.dsn, got, c.want)
			}
		})
	}
}

func TestPoolConfigKeepsAPoolSizeTheDSNChose(t *testing.T) {
	// The doc comment promises a deployment can tune the pool without a code
	// change, so an explicit value has to survive the service default.
	cfg, err := PoolConfig("postgres://app:secret@localhost:5432/core?pool_max_conns=25")
	if err != nil {
		t.Fatalf("PoolConfig() returned error: %v", err)
	}

	if cfg.MaxConns != 25 {
		t.Errorf("MaxConns = %d, want 25 from the DSN", cfg.MaxConns)
	}
}

// The provisioning pool runs CREATE DATABASE … TEMPLATE, which takes as long
// as copying the template takes; the core API's ten seconds would fail it for
// exactly the contests large enough to need it.
func TestMaintenancePoolConfigTakesItsOwnStatementTimeout(t *testing.T) {
	cfg, err := MaintenancePoolConfig("postgres://u:p@localhost:5432/game", 10*time.Minute)
	if err != nil {
		t.Fatalf("MaintenancePoolConfig() returned error: %v", err)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "600000" {
		t.Errorf("statement_timeout = %q, want 600000 (ten minutes in milliseconds)", got)
	}

	// The core default is untouched by the variant.
	core, err := PoolConfig("postgres://u:p@localhost:5432/core")
	if err != nil {
		t.Fatalf("PoolConfig() returned error: %v", err)
	}
	if got := core.ConnConfig.RuntimeParams["statement_timeout"]; got != "10000" {
		t.Errorf("core statement_timeout = %q, want 10000", got)
	}
}

// A deployment sizes the pool through CORE_DB_POOL_MAX (config.Config),
// applied here rather than baked into the DSN so the operator's own value and
// the service default can be told apart from "the DSN already decided".
func TestPoolConfigWithMaxConnsAppliesTheConfiguredSize(t *testing.T) {
	cfg, err := PoolConfigWithMaxConns("postgres://app:secret@localhost:5432/core", 40)
	if err != nil {
		t.Fatalf("PoolConfigWithMaxConns() returned error: %v", err)
	}
	if cfg.MaxConns != 40 {
		t.Errorf("MaxConns = %d, want 40 from the configured override", cfg.MaxConns)
	}
}

// A DSN that already tunes the pool itself is not second-guessed by the
// separate CORE_DB_POOL_MAX override — one deployment should not need to
// agree with itself twice about the same number.
func TestPoolConfigWithMaxConnsKeepsTheDSNsOwnSetting(t *testing.T) {
	cfg, err := PoolConfigWithMaxConns("postgres://app:secret@localhost:5432/core?pool_max_conns=7", 40)
	if err != nil {
		t.Fatalf("PoolConfigWithMaxConns() returned error: %v", err)
	}
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7 from the DSN", cfg.MaxConns)
	}
}

// Zero means "not configured": the deployment left CORE_DB_POOL_MAX unset,
// and the service's own default applies exactly as PoolConfig's does.
func TestPoolConfigWithMaxConnsFallsBackToTheServiceDefaultWhenUnset(t *testing.T) {
	cfg, err := PoolConfigWithMaxConns("postgres://app:secret@localhost:5432/core", 0)
	if err != nil {
		t.Fatalf("PoolConfigWithMaxConns() returned error: %v", err)
	}
	if cfg.MaxConns != defaultMaxConns {
		t.Errorf("MaxConns = %d, want the service default %d", cfg.MaxConns, defaultMaxConns)
	}
}
