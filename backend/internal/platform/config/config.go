// Package config loads service configuration from the process environment.
//
// Configuration is read once at startup and treated as immutable afterwards.
// Values that have no safe default (database and cache endpoints) are
// required, so a misconfigured deployment fails immediately instead of
// surfacing as a runtime error under load.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultInternalAddr is where metrics and health probes listen when
// INTERNAL_ADDR is not set. Exported so the container self-check derives its
// probe URL from the same constant and cannot drift from it.
const DefaultInternalAddr = ":9090"

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
	// CookieSecure marks the session cookie Secure. It defaults to true
	// outside development: a browser drops a Secure cookie over plain HTTP, so
	// a local stack without a certificate needs it off, and every other
	// deployment needs it on.
	CookieSecure bool
	// TrustedProxies lists the CIDRs (or bare addresses) whose forwarded
	// headers are believed when resolving the client IP. Empty means the TCP
	// peer is always the client — correct without a reverse proxy, and the
	// safe default behind an unknown one.
	TrustedProxies []string
	// DefaultLocale is the language of last resort, used when a request
	// expresses no usable preference and no contest narrows it down. It is a
	// BCP-47 tag matching a row in the `languages` table.
	DefaultLocale string
	// SessionTTL is how long a session survives without activity. It slides on
	// every authenticated request, so it bounds idle time rather than the
	// length of a working session.
	SessionTTL time.Duration
	// GameProvisionerDSN connects to the game cluster as the provisioning
	// role, which creates and drops participants' databases. Optional: empty
	// turns provisioning off, which is what a deployment without a game
	// cluster wants.
	//
	// Deliberately not the participant's credentials. The Query Runner is the
	// only process that ever connects as game_reader or game_writer, and this
	// role cannot be one of them — section 11 asks for separate passwords for
	// the core application, the provisioner and the participant roles, and
	// this is where two of the three stay apart.
	GameProvisionerDSN string
	// PoolDepth is how many spare copies each live contest keeps ready, so
	// that a participant arriving does not wait for CREATE DATABASE.
	PoolDepth int
	// ProvisionWorkers is how many copies are made at once. Section 4.2 says
	// two to four: enough to fill a pool in reasonable time, few enough that
	// filling it is never what the cluster is busy doing.
	ProvisionWorkers int
	// CopyStrategy is how PostgreSQL copies a template — empty leaves its own
	// default. Which of the two wins depends on the size of the template, so
	// it is a measurement rather than a constant.
	CopyStrategy string
	// MaxLoginAttemptsPerAddress caps sign-in attempts from one address in a
	// quarter of an hour. It counts successes too, so it bounds people and not
	// only guesses: a hall of students behind one NAT address is one address
	// here. Raise it where the whole cohort shares an address.
	MaxLoginAttemptsPerAddress int
}

// Load reads configuration from the environment, applying defaults for
// optional settings. It returns an error naming the offending variable when a
// required value is missing or a value cannot be parsed.
func Load() (Config, error) {
	cfg := Config{
		Env:          envOrDefault("ENV", "development"),
		HTTPAddr:     envOrDefault("HTTP_ADDR", ":8080"),
		InternalAddr: envOrDefault("INTERNAL_ADDR", DefaultInternalAddr),
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
	if cfg.SessionTTL, err = durationEnv("SESSION_TTL", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.CookieSecure, err = boolEnv("COOKIE_SECURE", cfg.Env != "development"); err != nil {
		return Config{}, err
	}

	// Zero means "not stated", and the authentication service supplies its own
	// default. The number is a rule about brute force, so it belongs to that
	// package rather than here — and this one must not import it: platform
	// packages do not depend on a domain (CLAUDE.md, Go layout rule 7).
	if cfg.MaxLoginAttemptsPerAddress, err = intEnv("MAX_LOGIN_ATTEMPTS_PER_ADDRESS", 0); err != nil {
		return Config{}, err
	}

	cfg.GameProvisionerDSN = os.Getenv("GAME_PROVISIONER_DSN")
	if cfg.PoolDepth, err = intEnv("GAME_POOL_DEPTH", 10); err != nil {
		return Config{}, err
	}
	if cfg.PoolDepth < 0 {
		return Config{}, fmt.Errorf("GAME_POOL_DEPTH cannot be negative, got %d", cfg.PoolDepth)
	}
	if cfg.ProvisionWorkers, err = intEnv("GAME_PROVISION_WORKERS", 3); err != nil {
		return Config{}, err
	}
	if cfg.ProvisionWorkers < 1 {
		return Config{}, fmt.Errorf("GAME_PROVISION_WORKERS must be at least 1, got %d", cfg.ProvisionWorkers)
	}
	cfg.CopyStrategy = os.Getenv("GAME_COPY_STRATEGY")

	cfg.DefaultLocale = envOrDefault("DEFAULT_LOCALE", "en")
	if !languageTag.MatchString(cfg.DefaultLocale) {
		return Config{}, fmt.Errorf("DEFAULT_LOCALE: %q is not a language tag", cfg.DefaultLocale)
	}

	// Validation happens here so a typo fails the boot; the list is split
	// eagerly and re-validated by the resolver that consumes it.
	if raw := os.Getenv("TRUSTED_PROXIES"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if err := validateProxyEntry(entry); err != nil {
				return Config{}, fmt.Errorf("TRUSTED_PROXIES: %w", err)
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, entry)
		}
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

// languageTag matches a BCP-47 tag loosely enough for the codes this platform
// uses ("en", "ro", "ru-KZ") and strictly enough to catch a typo before it
// becomes every participant's fallback.
var languageTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// validateProxyEntry accepts a CIDR prefix or a bare address.
func validateProxyEntry(entry string) error {
	if _, err := netip.ParsePrefix(entry); err == nil {
		return nil
	}
	if _, err := netip.ParseAddr(entry); err == nil {
		return nil
	}
	return fmt.Errorf("%q is neither a CIDR nor an address", entry)
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

// intEnv reads a whole number, refusing a negative one: every setting that
// uses it counts something.
func intEnv(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a whole number", key, raw)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s: %d is negative", key, value)
	}
	return value, nil
}

func boolEnv(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return value, nil
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
