package config

import (
	"strings"
	"testing"
)

func TestRedisAddressIsOptional(t *testing.T) {
	// A single-node install runs without Redis; the service must still start.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() without REDIS_ADDR returned error: %v", err)
	}

	if cfg.RedisAddr != "" {
		t.Errorf("RedisAddr = %q, want empty", cfg.RedisAddr)
	}
}

func TestCoreDatabaseRemainsRequired(t *testing.T) {
	// The core database has no fallback: without it there are no users,
	// contests or answers, so starting would be pointless.
	t.Setenv("REDIS_ADDR", "localhost:6379")

	_, err := Load()

	if err == nil {
		t.Fatal("Load() succeeded without CORE_DB_DSN, want error")
	}
}

func TestMetricsBackendDefaultsToPrometheus(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.MetricsBackend != "prometheus" {
		t.Errorf("MetricsBackend = %q, want prometheus", cfg.MetricsBackend)
	}
}

func TestMetricsBackendIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	for _, backend := range []string{"prometheus", "log", "none"} {
		t.Setenv("METRICS_BACKEND", backend)

		cfg, err := Load()
		if err != nil {
			t.Errorf("Load() with METRICS_BACKEND=%q returned error: %v", backend, err)
			continue
		}
		if cfg.MetricsBackend != backend {
			t.Errorf("MetricsBackend = %q, want %q", cfg.MetricsBackend, backend)
		}
	}
}

func TestUnknownMetricsBackendIsRejectedAtStartup(t *testing.T) {
	// Catching a typo at boot beats discovering at the first contest that
	// nothing was ever recorded.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("METRICS_BACKEND", "statsd")

	_, err := Load()

	if err == nil {
		t.Fatal("Load() accepted an unknown metrics backend, want error")
	}
	if !strings.Contains(err.Error(), "statsd") {
		t.Errorf("error %q does not name the offending value", err)
	}
}
