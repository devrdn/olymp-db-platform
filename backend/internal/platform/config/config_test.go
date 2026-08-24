package config

import (
	"strings"
	"testing"
	"time"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("REDIS_ADDR", "localhost:6379")
}

func TestLoadReadsValuesFromEnvironment(t *testing.T) {
	setRequired(t)
	t.Setenv("HTTP_ADDR", ":8081")
	t.Setenv("ENV", "production")
	t.Setenv("LOG_LEVEL", "warn")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTPAddr != ":8081" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8081")
	}
	if cfg.Env != "production" {
		t.Errorf("Env = %q, want %q", cfg.Env, "production")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "warn")
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 30*time.Second)
	}
	if cfg.CoreDBDSN != "postgres://user:pass@localhost:5432/core" {
		t.Errorf("CoreDBDSN = %q, unexpected", cfg.CoreDBDSN)
	}
}

func TestLoadReadsInternalListenerAddress(t *testing.T) {
	setRequired(t)
	t.Setenv("INTERNAL_ADDR", ":9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.InternalAddr != ":9999" {
		t.Errorf("InternalAddr = %q, want %q", cfg.InternalAddr, ":9999")
	}
}

func TestLoadRejectsInternalListenerSharingThePublicPort(t *testing.T) {
	// Metrics and readiness live on the internal listener precisely because
	// they must not be reachable from the public one.
	setRequired(t)
	t.Setenv("HTTP_ADDR", ":8080")
	t.Setenv("INTERNAL_ADDR", ":8080")

	_, err := Load()

	if err == nil {
		t.Fatal("Load() succeeded with both listeners on the same port, want error")
	}
}

func TestLoadAppliesDefaultsForOptionalValues(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.Env != "development" {
		t.Errorf("Env = %q, want default %q", cfg.Env, "development")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default %q", cfg.LogLevel, "info")
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want default %v", cfg.ShutdownTimeout, 15*time.Second)
	}
	if cfg.InternalAddr != ":9090" {
		t.Errorf("InternalAddr = %q, want default %q", cfg.InternalAddr, ":9090")
	}
}

func TestLoadFailsWhenRequiredVariableMissing(t *testing.T) {
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("CORE_DB_DSN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded, want error for missing CORE_DB_DSN")
	}
	if !strings.Contains(err.Error(), "CORE_DB_DSN") {
		t.Errorf("error %q does not name the missing variable CORE_DB_DSN", err)
	}
}

func TestLoadRejectsUnknownLogLevel(t *testing.T) {
	setRequired(t)
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded, want error for unknown log level")
	}
	if !strings.Contains(err.Error(), "verbose") {
		t.Errorf("error %q does not mention the offending value", err)
	}
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	setRequired(t)
	t.Setenv("SHUTDOWN_TIMEOUT", "soon")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded, want error for malformed duration")
	}
	if !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT") {
		t.Errorf("error %q does not name the offending variable", err)
	}
}
