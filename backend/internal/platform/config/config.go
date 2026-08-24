// Package config loads service configuration from the process environment.
//
// Configuration is read once at startup and treated as immutable afterwards.
// Values that have no safe default (database and cache endpoints) are
// required, so a misconfigured deployment fails immediately instead of
// surfacing as a runtime error under load.
package config

import (
	"fmt"
	"os"
	"slices"
	"time"
)

// validLogLevels mirrors the levels understood by the logging package.
var validLogLevels = []string{"debug", "info", "warn", "error"}

// validMetricsBackends mirrors the backends built by the metrics package.
var validMetricsBackends = []string{"prometheus", "log", "none"}

// Config holds the settings of the Core API service.
type Config struct {
	Env      string
	HTTPAddr string
	// InternalAddr serves metrics and health probes. It must stay on a port
	// the reverse proxy does not publish.
	InternalAddr    string
	LogLevel        string
	ShutdownTimeout time.Duration
	// CoreDBDSN is required: the service has nothing to serve without it.
	CoreDBDSN string
	// RedisAddr is optional. Empty selects the in-process cache, which is
	// correct for a single instance and wrong for several (see the cache
	// package).
	RedisAddr string
	// MetricsBackend is prometheus, log or none.
	MetricsBackend string
}

// Load reads configuration from the environment, applying defaults for
// optional settings. It returns an error naming the offending variable when a
// required value is missing or a value cannot be parsed.
func Load() (Config, error) {
	cfg := Config{
		Env:          envOrDefault("ENV", "development"),
		HTTPAddr:     envOrDefault("HTTP_ADDR", ":8080"),
		InternalAddr: envOrDefault("INTERNAL_ADDR", ":9090"),
		LogLevel:     envOrDefault("LOG_LEVEL", "info"),
	}

	var err error
	if cfg.CoreDBDSN, err = requiredEnv("CORE_DB_DSN"); err != nil {
		return Config{}, err
	}
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.MetricsBackend = envOrDefault("METRICS_BACKEND", "prometheus")

	if cfg.ShutdownTimeout, err = durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}

	if !slices.Contains(validMetricsBackends, cfg.MetricsBackend) {
		return Config{}, fmt.Errorf("METRICS_BACKEND: unknown backend %q, want one of %v",
			cfg.MetricsBackend, validMetricsBackends)
	}

	if !slices.Contains(validLogLevels, cfg.LogLevel) {
		return Config{}, fmt.Errorf("LOG_LEVEL: unknown level %q, want one of %v", cfg.LogLevel, validLogLevels)
	}

	// Sharing a port would publish metrics and readiness on the public
	// listener, which is exactly what the split listener prevents.
	if cfg.HTTPAddr == cfg.InternalAddr {
		return Config{}, fmt.Errorf("INTERNAL_ADDR: must differ from HTTP_ADDR (both are %q)", cfg.HTTPAddr)
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requiredEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("%s: required environment variable is not set", key)
	}
	return v, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
