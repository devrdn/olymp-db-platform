// Package config loads service configuration from the environment, once at
// startup. Values with no safe default are required and every value is
// validated, so a misconfigured deployment fails at boot rather than under
// load. It imports no domain package.
package config

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/password"
)

// DefaultInternalAddr is where metrics and health probes listen when
// INTERNAL_ADDR is not set; the container self-check uses it too.
const DefaultInternalAddr = ":9090"

// maxPasswordHashWait bounds PASSWORD_HASH_MAX_WAIT, so a sign-in flood is
// answered rather than parked.
const maxPasswordHashWait = 30 * time.Second

// Bounds on SESSION_MAX_LIFETIME.
const (
	minSessionMaxLifetime = 5 * time.Minute
	maxSessionMaxLifetime = 7 * 24 * time.Hour
)

// maxSessionAccountCacheTTL bounds SESSION_ACCOUNT_CACHE_TTL: how long an
// account change the cache was not told about (made by hand in the database)
// goes unnoticed. Past half a minute a block would no longer be immediate.
const maxSessionAccountCacheTTL = 30 * time.Second

// Device cookie bounds. The secret's minimum is the size of its HMAC-SHA256
// key; past ninety days a browser handed on to somebody else keeps the trust.
const (
	minDeviceCookieSecretBytes     = 32
	minDeviceCookieTTL             = time.Hour
	maxDeviceCookieTTL             = 90 * 24 * time.Hour
	maxTrustedLoginAttemptsCeiling = 1000
)

// deviceCookieSecret reads DEVICE_COOKIE_SECRET, or generates one in
// development. Elsewhere it is required: a generated key would change at each
// restart and differ between replicas.
func deviceCookieSecret(env string) ([]byte, error) {
	raw := os.Getenv("DEVICE_COOKIE_SECRET")
	if raw == "" && env == "development" {
		secret := make([]byte, minDeviceCookieSecretBytes)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("DEVICE_COOKIE_SECRET: generate a development secret: %w", err)
		}
		return secret, nil
	}
	if raw == "" {
		return nil, errors.New("DEVICE_COOKIE_SECRET: required outside development")
	}
	if err := RefusePlaceholder(env, "DEVICE_COOKIE_SECRET", raw); err != nil {
		return nil, err
	}
	if len(raw) < minDeviceCookieSecretBytes {
		return nil, fmt.Errorf("DEVICE_COOKIE_SECRET: %d bytes, at least %d are required", len(raw), minDeviceCookieSecretBytes)
	}
	return []byte(raw), nil
}

const placeholderMarker = "change-me"

// RefusePlaceholder refuses, outside development, a credential still carrying
// deploy/.env.example's placeholder, which anybody who read the repository
// knows. The error names the variable, never the value. Exported for
// cmd/gamedb.
func RefusePlaceholder(env, name, value string) error {
	if env == "development" || !strings.Contains(strings.ToLower(value), placeholderMarker) {
		return nil
	}
	return fmt.Errorf("%s: still carries the placeholder from deploy/.env.example; "+
		"set a generated value (required outside development)", name)
}

// Query Runner token bounds: at least 256 bits; the maximum keeps a pasted
// file out of a header.
const (
	minQueryRunnerTokenBytes = 32
	maxQueryRunnerTokenBytes = 512
)

// queryRunnerToken reads QUERY_RUNNER_TOKEN for both the Core API and the
// Query Runner, so they agree on what a valid token is. Outside development a
// missing token is refused when required. It must be printable ASCII to
// travel as gRPC metadata. No error repeats the value.
func queryRunnerToken(env string, required bool) (string, error) {
	raw := os.Getenv("QUERY_RUNNER_TOKEN")
	if raw == "" {
		if required && env != "development" {
			return "", errors.New("QUERY_RUNNER_TOKEN: required outside development " +
				"(the Query Runner answers only callers holding it)")
		}
		return "", nil
	}
	if err := RefusePlaceholder(env, "QUERY_RUNNER_TOKEN", raw); err != nil {
		return "", err
	}
	if len(raw) < minQueryRunnerTokenBytes || len(raw) > maxQueryRunnerTokenBytes {
		return "", fmt.Errorf("QUERY_RUNNER_TOKEN: %d bytes, want between %d and %d",
			len(raw), minQueryRunnerTokenBytes, maxQueryRunnerTokenBytes)
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '!' || raw[i] > '~' {
			return "", fmt.Errorf("QUERY_RUNNER_TOKEN: byte %d is not printable ASCII "+
				"(no spaces or control characters; `openssl rand -hex 32` produces a valid one)", i)
		}
	}
	return raw, nil
}

// maxLoginAttemptsCeiling bounds MAX_LOGIN_ATTEMPTS_PER_ACCOUNT.
const maxLoginAttemptsCeiling = 100_000

// maxCoreDBPoolMax bounds CORE_DB_POOL_MAX at pg-core's default
// max_connections; raising it is a code change alongside the cluster's.
const maxCoreDBPoolMax = 100

// maxExportConcurrency bounds EXPORT_CONCURRENCY: two fifths of the default
// pool of 25, so sign-in never queues behind slow downloads.
const maxExportConcurrency = 10

// maxExportsForPool applies the same two-fifths rule to the pool size a
// deployment set, since each ceiling alone allows combinations (ten exports,
// a pool of five) that exceed the pool.
func maxExportsForPool(poolMax int) int { return poolMax * 2 / 5 }

var validLogLevels = []string{"debug", "info", "warn", "error"}

var validMetricsBackends = []string{"prometheus", "log", "none"}

type Config struct {
	Env      string
	HTTPAddr string
	// InternalAddr serves metrics and health probes, on a port the reverse
	// proxy must not publish.
	InternalAddr    string
	LogLevel        string
	ShutdownTimeout time.Duration
	CoreDBDSN       string
	// CoreDBPoolMax overrides the core database pool size; zero keeps the
	// storage default. Bounded by maxCoreDBPoolMax.
	CoreDBPoolMax int
	// ExportConcurrency is how many CSV downloads may hold a pool connection
	// at once; a download holds it as long as the client takes to read.
	// Zero keeps api.DefaultExportConcurrency.
	ExportConcurrency int
	// RedisAddr empty selects the in-process cache, correct for a single
	// instance only.
	RedisAddr      string
	MetricsBackend string
	// CookieSecure marks the session cookie Secure. It defaults to true
	// outside development; a local stack without TLS needs it off.
	CookieSecure bool
	// TrustedProxies lists the CIDRs (or bare addresses) whose forwarded
	// headers are believed (CLAUDE.md rule 9). Empty means the TCP peer is
	// always the client, the safe default.
	TrustedProxies []string
	// PublicOrigins names front origins this deployment answers for besides
	// its own host (httpx.CheckOrigin). Empty is the strict default; set it
	// where the interface and the API have different hosts, as in a
	// development stack.
	PublicOrigins []string
	// DefaultLocale is the language of last resort: a BCP-47 tag matching a
	// row in the `languages` table.
	DefaultLocale string
	// SessionTTL bounds idle time; it slides on every authenticated request.
	SessionTTL time.Duration
	// SessionMaxLifetime bounds a session from sign-in however active it is,
	// so a copied cookie in constant use still expires.
	SessionMaxLifetime time.Duration
	// SessionAccountCacheTTL is how long authentication may use a cached
	// account. Changes made through the service invalidate it at once; this
	// bounds any other change. Zero reads the account on every request.
	SessionAccountCacheTTL time.Duration
	// DeviceCookieSecret keys the HMAC of the device cookie, which marks a
	// browser an account's owner has signed in from.
	DeviceCookieSecret []byte
	DeviceCookieTTL    time.Duration
	// MaxLoginAttemptsPerDevice caps sign-in attempts through one trusted
	// browser per 15 minutes. Zero leaves it to the auth package.
	MaxLoginAttemptsPerDevice int
	// MaxTrustedLoginAttemptsPerAccount caps attempts through all of one
	// account's trusted browsers per 15 minutes. Zero leaves it to auth.
	MaxTrustedLoginAttemptsPerAccount int
	// GameProvisionerDSN connects to the game cluster as the provisioning
	// role; empty turns provisioning off. It is never a participant role:
	// only the Query Runner connects as game_reader or game_writer.
	GameProvisionerDSN string
	// GameAuthorPassword authenticates game_author, the role an organiser's
	// game script runs as. Required with GameProvisionerDSN: the alternative
	// is running staff SQL with the provisioner's privileges.
	GameAuthorPassword string
	// GameBuildTimeout bounds one run of an organiser's game script: both
	// its context deadline and the build connection's statement_timeout
	// (CLAUDE.md rule 15). The role's own default is unbounded so this governs.
	GameBuildTimeout time.Duration
	// PoolDepth is the headroom each live contest keeps ready beyond the
	// participants still without a copy, so a late enrolment does not wait
	// for CREATE DATABASE.
	PoolDepth int
	// PoolMax caps how many copies one contest may hold; zero means no cap.
	// A count does not bound disk; ClusterMaxBytes does.
	PoolMax int
	// ClusterMaxBytes is how much disk all databases on the game cluster may
	// occupy together. Zero means no budget, which lets self-enrolment fill
	// the disk. The deployment sets it from the volume size, which
	// PostgreSQL does not expose portably; the default is not unlimited, so
	// a larger installation learns from a log line, not a full volume.
	ClusterMaxBytes int64
	// QueryRunnerAddr is the Query Runner's address; empty turns the SQL
	// console off.
	QueryRunnerAddr string
	// QueryRunnerToken is the shared secret every Query Runner call carries.
	// Required outside development when QueryRunnerAddr is set; never logged.
	QueryRunnerToken string
	// ProvisionWorkers is how many copies are made at once (two to four).
	ProvisionWorkers int
	// CopyStrategy is how PostgreSQL copies a template; empty keeps its
	// default. The faster one depends on the template's size.
	CopyStrategy string
	// MaxLoginAttemptsPerAddress caps sign-in attempts from one address per
	// 15 minutes, successes included. Raise it where a whole hall shares one
	// NAT address.
	MaxLoginAttemptsPerAddress int
	// MaxLoginAttemptsPerAccount caps attempts at one account from all
	// addresses per 15 minutes, against guessing spread across addresses.
	// Zero leaves it to the auth package.
	MaxLoginAttemptsPerAccount int
	// PasswordHashConcurrency is how many argon2id computations run at once,
	// 64 MiB each, so the memory limit is sized from it. Zero leaves it to
	// the password package.
	PasswordHashConcurrency int
	PasswordHashMaxWait     time.Duration
	// QueryPerMinute is a participant's read budget when the contest sets
	// none. It must equal the Query Runner's QUERY_PER_MINUTE (Runner.
	// PerMinute), including what zero means: unset is 30 on both sides, an
	// explicit 0 is no limit on both. The events channel and read endpoints
	// spend the same budget (queryproxy.Service.AdmitRead), so raising it
	// raises that ceiling too.
	QueryPerMinute int
	// AnswerRatePerMinute is how many answers one registration may submit
	// per minute, refused ones included. It is separate from QueryPerMinute
	// because an answer is a guess. Bounded to 1-60, with no "unlimited".
	AnswerRatePerMinute int
	// DeadlineGrace is the network-latency allowance added to a
	// participant's deadline. It is handed to the participation gate, the one
	// place deadlines are checked.
	DeadlineGrace time.Duration
	// GameInstanceGraceMin is how long participant databases survive a
	// finished contest when its own grace_period_min is zero. Zero here is
	// not "no limit": it reclaims on the next tick.
	GameInstanceGraceMin int
	// GameUploadDir holds uploaded SQL dumps (internal/gamefile). Empty turns
	// file uploads off.
	GameUploadDir string
	// GameUploadMaxFileBytes bounds one upload's total size, set above the
	// expected 1-3 GB so a grown script is slow rather than refused.
	GameUploadMaxFileBytes int64
	// GameUploadMaxDirBytes bounds every upload the volume holds together.
	GameUploadMaxDirBytes int64
	// GameUploadTableMaxDirBytes bounds the table builder's CSV store, a
	// separate directory on the same volume. It is sized on its own so the
	// two stores together do not silently double GameUploadMaxDirBytes.
	GameUploadTableMaxDirBytes int64
	// GameUploadChunkBytes bounds one Append call (CLAUDE.md rule 12).
	GameUploadChunkBytes int64
	// GameUploadAbandonedAfter is how long an upload may sit idle before the
	// janitor aborts it and frees the disk.
	GameUploadAbandonedAfter time.Duration
	// CoverDir holds uploaded cover pictures (filestore). Unlike
	// GameUploadDir it cannot be turned off; an unwritable directory fails
	// startup.
	CoverDir string
}

// DefaultCoverDir is the COVER_DIR default, the api service's mount point in
// deploy/docker-compose.yml. cmd/gameorphans reads it too.
const DefaultCoverDir = "/var/lib/dbcontest/covers"

// Load reads configuration from the environment. An error names the
// offending variable.
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
	if cfg.CoreDBPoolMax, err = intEnv("CORE_DB_POOL_MAX", 0); err != nil {
		return Config{}, err
	}
	if cfg.CoreDBPoolMax < 0 || cfg.CoreDBPoolMax > maxCoreDBPoolMax {
		return Config{}, fmt.Errorf("CORE_DB_POOL_MAX: %d is outside [0, %d]", cfg.CoreDBPoolMax, maxCoreDBPoolMax)
	}
	if cfg.ExportConcurrency, err = intEnv("EXPORT_CONCURRENCY", 0); err != nil {
		return Config{}, err
	}
	if cfg.ExportConcurrency < 0 || cfg.ExportConcurrency > maxExportConcurrency {
		return Config{}, fmt.Errorf("EXPORT_CONCURRENCY: %d is outside [0, %d]",
			cfg.ExportConcurrency, maxExportConcurrency)
	}
	// Against the pool it shares, when one was set; maxExportConcurrency is
	// already sized for the default.
	if cfg.CoreDBPoolMax > 0 && cfg.ExportConcurrency > maxExportsForPool(cfg.CoreDBPoolMax) {
		return Config{}, fmt.Errorf(
			"EXPORT_CONCURRENCY: %d is more than downloads may hold of CORE_DB_POOL_MAX=%d (at most %d); raise the pool or lower the downloads",
			cfg.ExportConcurrency, cfg.CoreDBPoolMax, maxExportsForPool(cfg.CoreDBPoolMax))
	}
	cfg.RedisAddr = os.Getenv("REDIS_ADDR")
	cfg.MetricsBackend = envOrDefault("METRICS_BACKEND", "prometheus")

	if cfg.ShutdownTimeout, err = durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.SessionTTL, err = durationEnv("SESSION_TTL", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.SessionMaxLifetime, err = durationEnv("SESSION_MAX_LIFETIME", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.SessionMaxLifetime < minSessionMaxLifetime || cfg.SessionMaxLifetime > maxSessionMaxLifetime {
		return Config{}, fmt.Errorf("SESSION_MAX_LIFETIME: %s is outside [%s, %s]",
			cfg.SessionMaxLifetime, minSessionMaxLifetime, maxSessionMaxLifetime)
	}
	if cfg.SessionAccountCacheTTL, err = durationEnv("SESSION_ACCOUNT_CACHE_TTL", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.SessionAccountCacheTTL < 0 || cfg.SessionAccountCacheTTL > maxSessionAccountCacheTTL {
		return Config{}, fmt.Errorf("SESSION_ACCOUNT_CACHE_TTL: %s is outside [0s, %s]",
			cfg.SessionAccountCacheTTL, maxSessionAccountCacheTTL)
	}
	if cfg.CookieSecure, err = boolEnv("COOKIE_SECURE", cfg.Env != "development"); err != nil {
		return Config{}, err
	}
	if cfg.DeviceCookieSecret, err = deviceCookieSecret(cfg.Env); err != nil {
		return Config{}, err
	}
	if cfg.DeviceCookieTTL, err = durationEnv("DEVICE_COOKIE_TTL", 30*24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.DeviceCookieTTL < minDeviceCookieTTL || cfg.DeviceCookieTTL > maxDeviceCookieTTL {
		return Config{}, fmt.Errorf("DEVICE_COOKIE_TTL: %s is outside [%s, %s]",
			cfg.DeviceCookieTTL, minDeviceCookieTTL, maxDeviceCookieTTL)
	}
	if cfg.MaxLoginAttemptsPerDevice, err = intEnv("MAX_LOGIN_ATTEMPTS_PER_DEVICE", 0); err != nil {
		return Config{}, err
	}
	if cfg.MaxLoginAttemptsPerDevice > maxTrustedLoginAttemptsCeiling {
		return Config{}, fmt.Errorf("MAX_LOGIN_ATTEMPTS_PER_DEVICE: %d is above the ceiling of %d",
			cfg.MaxLoginAttemptsPerDevice, maxTrustedLoginAttemptsCeiling)
	}
	if cfg.MaxTrustedLoginAttemptsPerAccount, err = intEnv("MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT", 0); err != nil {
		return Config{}, err
	}
	if cfg.MaxTrustedLoginAttemptsPerAccount > maxTrustedLoginAttemptsCeiling {
		return Config{}, fmt.Errorf("MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT: %d is above the ceiling of %d",
			cfg.MaxTrustedLoginAttemptsPerAccount, maxTrustedLoginAttemptsCeiling)
	}

	// Zero means "not stated": the auth package supplies the default, which
	// this platform package cannot import (CLAUDE.md layout rule 7).
	if cfg.MaxLoginAttemptsPerAddress, err = intEnv("MAX_LOGIN_ATTEMPTS_PER_ADDRESS", 0); err != nil {
		return Config{}, err
	}
	// Bounded: an unreachable ceiling is no backstop.
	if cfg.MaxLoginAttemptsPerAccount, err = intEnv("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT", 0); err != nil {
		return Config{}, err
	}
	if cfg.MaxLoginAttemptsPerAccount > maxLoginAttemptsCeiling {
		return Config{}, fmt.Errorf("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT: %d is above the ceiling of %d",
			cfg.MaxLoginAttemptsPerAccount, maxLoginAttemptsCeiling)
	}
	if cfg.PasswordHashConcurrency, err = intEnv("PASSWORD_HASH_CONCURRENCY", 0); err != nil {
		return Config{}, err
	}
	if cfg.PasswordHashConcurrency > password.MaxConcurrency {
		return Config{}, fmt.Errorf("PASSWORD_HASH_CONCURRENCY: %d is above the ceiling of %d",
			cfg.PasswordHashConcurrency, password.MaxConcurrency)
	}
	if cfg.PasswordHashMaxWait, err = durationEnv("PASSWORD_HASH_MAX_WAIT", password.DefaultMaxWait); err != nil {
		return Config{}, err
	}
	if cfg.PasswordHashMaxWait <= 0 || cfg.PasswordHashMaxWait > maxPasswordHashWait {
		return Config{}, fmt.Errorf("PASSWORD_HASH_MAX_WAIT: %s is outside (0, %s]",
			cfg.PasswordHashMaxWait, maxPasswordHashWait)
	}

	// 30 unset, matching LoadRunner (see the field).
	if cfg.QueryPerMinute, err = intEnv("QUERY_PER_MINUTE", 30); err != nil {
		return Config{}, err
	}
	if cfg.AnswerRatePerMinute, err = intEnv("ANSWER_RATE_PER_MINUTE", defaultAnswerRatePerMinute); err != nil {
		return Config{}, err
	}
	if cfg.AnswerRatePerMinute < 1 || cfg.AnswerRatePerMinute > maxAnswerRatePerMinute {
		return Config{}, fmt.Errorf("ANSWER_RATE_PER_MINUTE: %d is outside 1-%d",
			cfg.AnswerRatePerMinute, maxAnswerRatePerMinute)
	}
	if cfg.DeadlineGrace, err = durationEnv("DEADLINE_GRACE", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.DeadlineGrace < 0 {
		return Config{}, fmt.Errorf("DEADLINE_GRACE cannot be negative, got %s", cfg.DeadlineGrace)
	}

	cfg.GameProvisionerDSN = os.Getenv("GAME_PROVISIONER_DSN")
	cfg.GameAuthorPassword = os.Getenv("GAME_AUTHOR_PASSWORD")
	// Every credential this process reads, whole or inside a URL.
	for name, value := range map[string]string{
		"CORE_DB_DSN":          cfg.CoreDBDSN,
		"REDIS_ADDR":           cfg.RedisAddr,
		"GAME_PROVISIONER_DSN": cfg.GameProvisionerDSN,
		"GAME_AUTHOR_PASSWORD": cfg.GameAuthorPassword,
	} {
		if err := RefusePlaceholder(cfg.Env, name, value); err != nil {
			return Config{}, err
		}
	}
	if cfg.GameProvisionerDSN != "" && cfg.GameAuthorPassword == "" {
		return Config{}, fmt.Errorf(
			"GAME_AUTHOR_PASSWORD is required when GAME_PROVISIONER_DSN is set: " +
				"a game script runs as the game_author role and not as the provisioner")
	}
	// Thirty minutes unset. The provisioning pool's shorter timeout bounds
	// CREATE and DROP DATABASE, not an organiser's SQL.
	if cfg.GameBuildTimeout, err = durationEnv("GAME_BUILD_TIMEOUT", 30*time.Minute); err != nil {
		return Config{}, err
	}
	if cfg.GameBuildTimeout <= 0 {
		return Config{}, fmt.Errorf("GAME_BUILD_TIMEOUT must be positive, got %s", cfg.GameBuildTimeout)
	}
	if cfg.PoolMax, err = intEnv("GAME_POOL_MAX", 500); err != nil {
		return Config{}, err
	}
	if cfg.PoolMax < 0 {
		return Config{}, fmt.Errorf("GAME_POOL_MAX cannot be negative, got %d", cfg.PoolMax)
	}
	if cfg.ClusterMaxBytes, err = int64Env("GAME_CLUSTER_MAX_BYTES", 64<<30); err != nil {
		return Config{}, err
	}
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
	cfg.QueryRunnerAddr = os.Getenv("QUERY_RUNNER_ADDR")
	if cfg.QueryRunnerToken, err = queryRunnerToken(cfg.Env, cfg.QueryRunnerAddr != ""); err != nil {
		return Config{}, err
	}
	// The two secrets guard different things, so one value in both would
	// turn a leak of either into both. Compared in constant time.
	if cfg.QueryRunnerToken != "" &&
		subtle.ConstantTimeCompare(cfg.DeviceCookieSecret, []byte(cfg.QueryRunnerToken)) == 1 {
		return Config{}, errors.New("DEVICE_COOKIE_SECRET and QUERY_RUNNER_TOKEN must be different secrets")
	}
	if cfg.GameInstanceGraceMin, err = intEnv("GAME_INSTANCE_GRACE_MIN", 24*60); err != nil {
		return Config{}, err
	}
	if cfg.GameInstanceGraceMin < 0 {
		return Config{}, fmt.Errorf("GAME_INSTANCE_GRACE_MIN cannot be negative, got %d", cfg.GameInstanceGraceMin)
	}

	cfg.GameUploadDir = os.Getenv("GAME_UPLOAD_DIR")
	if cfg.GameUploadMaxFileBytes, err = int64Env("GAME_UPLOAD_MAX_FILE_BYTES", 4<<30); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadMaxDirBytes, err = int64Env("GAME_UPLOAD_MAX_DIR_BYTES", 16<<30); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadTableMaxDirBytes, err = int64Env("GAME_UPLOAD_TABLE_MAX_DIR_BYTES", 4<<30); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadChunkBytes, err = int64Env("GAME_UPLOAD_CHUNK_BYTES", 8<<20); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadAbandonedAfter, err = durationEnv("GAME_UPLOAD_ABANDONED_AFTER", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadAbandonedAfter < 0 {
		return Config{}, fmt.Errorf("GAME_UPLOAD_ABANDONED_AFTER cannot be negative, got %s", cfg.GameUploadAbandonedAfter)
	}
	if cfg.GameUploadDir != "" {
		if cfg.GameUploadMaxFileBytes <= 0 || cfg.GameUploadMaxDirBytes <= 0 || cfg.GameUploadChunkBytes <= 0 ||
			cfg.GameUploadTableMaxDirBytes <= 0 {
			return Config{}, fmt.Errorf(
				"GAME_UPLOAD_MAX_FILE_BYTES, GAME_UPLOAD_MAX_DIR_BYTES, GAME_UPLOAD_TABLE_MAX_DIR_BYTES and " +
					"GAME_UPLOAD_CHUNK_BYTES must all be positive when GAME_UPLOAD_DIR is set")
		}
	}

	cfg.CoverDir = envOrDefault("COVER_DIR", DefaultCoverDir)

	cfg.DefaultLocale = envOrDefault("DEFAULT_LOCALE", "en")
	if !languageTag.MatchString(cfg.DefaultLocale) {
		return Config{}, fmt.Errorf("DEFAULT_LOCALE: %q is not a language tag", cfg.DefaultLocale)
	}

	// Validated here so a typo fails the boot.
	if raw := os.Getenv("PUBLIC_ORIGINS"); raw != "" {
		for _, origin := range strings.Split(raw, ",") {
			origin = strings.TrimSpace(origin)
			if origin == "" {
				continue
			}
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return Config{}, fmt.Errorf("PUBLIC_ORIGINS: %q is not a scheme://host origin", origin)
			}
			cfg.PublicOrigins = append(cfg.PublicOrigins, parsed.Scheme+"://"+parsed.Host)
		}
	}

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

	// Sharing a port would publish metrics and readiness publicly.
	if cfg.HTTPAddr == cfg.InternalAddr {
		return Config{}, fmt.Errorf("INTERNAL_ADDR: must differ from HTTP_ADDR (both are %q)", cfg.HTTPAddr)
	}

	return cfg, nil
}

// languageTag matches a BCP-47 tag loosely ("en", "ro", "ru-KZ"), enough to
// catch a typo.
var languageTag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

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

const (
	defaultAnswerRatePerMinute = 6
	maxAnswerRatePerMinute     = 60
)

// intEnv reads a non-negative whole number.
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

// int64Env is intEnv for byte quantities.
func int64Env(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a whole number of bytes", key, raw)
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
