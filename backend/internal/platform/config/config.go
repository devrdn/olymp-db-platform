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
// INTERNAL_ADDR is not set. Exported so the container self-check derives its
// probe URL from the same constant and cannot drift from it.
const DefaultInternalAddr = ":9090"

// maxPasswordHashWait bounds PASSWORD_HASH_MAX_WAIT. Past this, a flood of
// sign-in attempts is a flood of parked requests rather than of answers.
const maxPasswordHashWait = 30 * time.Second

// Bounds on SESSION_MAX_LIFETIME: below a few minutes nobody could sign in and
// get anything done, and past a week the limit no longer limits anything.
const (
	minSessionMaxLifetime = 5 * time.Minute
	maxSessionMaxLifetime = 7 * 24 * time.Hour
)

// maxLoginAttemptsCeiling bounds MAX_LOGIN_ATTEMPTS_PER_ACCOUNT.
const maxLoginAttemptsCeiling = 100_000

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
	// PublicOrigins names the front origins this deployment answers for, on
	// top of its own host, for httpx.CheckOrigin. Empty is the strict default
	// and what the compose deployment wants: Caddy passes the browser's Host
	// through, so the API's own host already is the origin the page came from.
	//
	// Set it where that identity does not hold. A development stack is the
	// common case — the browser is on :3000, Next's rewrite forwards /api/*
	// to :8080 and replaces Host on the way — and so is a production split
	// that answers the interface and the API on different names. The one
	// request a browser makes to this API directly is a chunk of an uploaded
	// dump; every other write goes through a server action and carries no
	// Origin at all, which is why nothing noticed until uploads existed.
	PublicOrigins []string
	// DefaultLocale is the language of last resort, used when a request
	// expresses no usable preference and no contest narrows it down. It is a
	// BCP-47 tag matching a row in the `languages` table.
	DefaultLocale string
	// SessionTTL is how long a session survives without activity. It slides on
	// every authenticated request, so it bounds idle time rather than the
	// length of a working session.
	SessionTTL time.Duration
	// SessionMaxLifetime is how long a session may exist from sign-in,
	// however actively it is used. SessionTTL alone never ends a session
	// somebody keeps using, including somebody using a copied cookie.
	SessionMaxLifetime time.Duration
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
	// GameAuthorPassword authenticates the game cluster's game_author role,
	// which an organiser's uploaded game script runs as. Required wherever
	// GameProvisionerDSN is set, because without it a template cannot be
	// built at all — and the alternative to that refusal is running staff SQL
	// with the provisioning role's own privileges, which is the whole thing
	// the separate role exists to stop (gamedb.RoleAuthor).
	//
	// The third of the three passwords section 11 asks to be kept apart: the
	// core application's, the provisioner's, and the participants'. This one
	// belongs to the same process as the provisioner's and is deliberately
	// still its own, because what it buys is exactly that the two are not
	// interchangeable.
	GameAuthorPassword string
	// GameBuildTimeout bounds one call to run an organiser's game script —
	// gamedb.Provisioner.runScript's own context deadline and the
	// statement_timeout it sets on the build's connection, deliberately kept
	// equal (CLAUDE.md rule 15). The role's own session default is
	// unbounded (authorDefaults, gamedb/cluster.go) precisely so this figure
	// is the one that governs, and a deployment building larger games than
	// the default expects raises it rather than editing a role.
	GameBuildTimeout time.Duration
	// PoolDepth is the headroom each live contest keeps ready *beyond* the
	// participants who already hold no copy — what makes a late enrolment
	// free rather than a wait. It is no longer the whole depth: sizing the
	// pool by a flat number was a bet that no more than that many people
	// turned up, and losing it meant everybody past it waiting for CREATE
	// DATABASE inside their own page load.
	PoolDepth int
	// PoolMax caps what one contest may ask the game cluster to hold, so a
	// mistyped roster cannot fill a disk. Zero means no cap.
	//
	// The cheap half of that bound and not the real one: five hundred copies
	// is ten gibibytes of a twenty-mebibyte template and a terabyte of a
	// two-gibibyte one, from the same number. ClusterMaxBytes below is what
	// actually bounds the disk.
	PoolMax int
	// ClusterMaxBytes is how much disk every database on the game cluster may
	// occupy together, this platform's and anything else sharing it. Zero
	// means no byte budget, which is the state that made an open contest's
	// roster — written by whoever self-enrols — a lever on the disk every
	// olympiad shares.
	//
	// A figure a deployment sets from the volume the cluster sits on, because
	// nothing PostgreSQL exposes portably says how much free space is under
	// its data directory. The default is sized for what this platform
	// describes — a few hundred participants and a template measured in tens
	// of megabytes — with room to spare, and is deliberately not "unlimited":
	// an installation whose games are larger than that should find out from a
	// log line saying the pool stopped growing and why, rather than from a
	// full volume during an olympiad.
	ClusterMaxBytes int64
	// QueryRunnerAddr is where the Query Runner service answers. Empty turns
	// the SQL console off, which is what a deployment without a game cluster
	// wants — and what one has before the runner is deployed.
	QueryRunnerAddr string
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
	// MaxLoginAttemptsPerAccount caps sign-in attempts at one account from
	// every address together in a quarter of an hour: the backstop against a
	// guess spread across many addresses, since the guessing limit itself is
	// per account and address. Zero leaves it to the auth package's default.
	MaxLoginAttemptsPerAccount int
	// PasswordHashConcurrency is how many argon2id computations the process
	// runs at once, across sign-in, password changes and account management.
	// Each holds 64 MiB, so this is a memory figure: the deployment's memory
	// limit is sized from it. Zero leaves it to the password package's
	// default (one per CPU, at least two).
	PasswordHashConcurrency int
	// PasswordHashMaxWait is how long a sign-in or password change waits for
	// a hashing slot before it is answered "busy".
	PasswordHashMaxWait time.Duration
	// QueryPerMinute is how often a participant with no contest-specific rate
	// may ask, for the console's own pre-check ahead of the query journal.
	// The number is a rule about the SQL console's load, so it belongs to
	// that package rather than here (see MaxLoginAttemptsPerAddress above for
	// the same reasoning). It exists at all so this figure can be kept equal
	// to the Query Runner's own QUERY_PER_MINUTE (internal/platform/config's
	// Runner.PerMinute), which this process never reads — and "equal" now
	// includes what zero means: leaving the variable unset defaults both
	// processes to the architecture's own 30, and setting it to 0 explicitly
	// means no limit on both sides, rather than "no limit" on one and
	// "unstated, use 30" on the other. A pre-check that believed a looser
	// number than the Query Runner would actually enforce used to let a
	// contest's own rate exceed the installation's without the journal write
	// it costs ever refusing anything (queryproxy.effectiveRateLimit is where
	// the two are reconciled).
	//
	// It is not only a query ceiling any more, either. queryproxy.Service's
	// events channel and its own read endpoints (the story, the question
	// list) now spend this same account-wide budget, under the same key, so
	// that a participant who alternates between running queries and polling
	// those endpoints cannot spend two budgets that add up to more load than
	// one (queryproxy.Service.AdmitRead's own doc). An operator raising this
	// number to give the SQL console more headroom is raising the ceiling on
	// that other traffic too, not just on queries.
	QueryPerMinute int
	// DeadlineGrace is the network-latency allowance added to a participant's
	// deadline (docs/ARCHITECTURE.md §8) before an action arriving after it is
	// refused. It exists because a request sent an instant before the
	// deadline can arrive an instant after it; the number is a rule about
	// timing, not about this process, so — like QueryPerMinute above — it
	// belongs to the package that enforces it (queryproxy.Service.WithGrace)
	// rather than to a constant duplicated wherever a deadline is checked.
	DeadlineGrace time.Duration
	// GameInstanceGraceMin is what a contest's own settings.grace_period_min
	// defers to when it is left at zero (docs/ARCHITECTURE.md §2.4, §4.2):
	// the installation's own answer to "how long after a contest finishes
	// does a participant's database survive", for every contest an organizer
	// never configured one for. The same convention QueryPerMinute documents
	// above, applied by provisioning.effectiveGrace instead of
	// queryproxy.effectiveRateLimit. Unlike that one, zero here does not mean
	// "no limit" — a grace of zero would reclaim a just-finished contest's
	// databases on the very next tick, which is a deliberate, aggressive
	// choice an operator makes on purpose, not a default nobody asked for.
	GameInstanceGraceMin int
	// GameUploadDir is where an organiser's uploaded SQL dump lands while it
	// is being received, and stays once it is complete — one directory on
	// the API host's own disk (internal/gamefile). Empty turns file uploads
	// off, the same convention QueryRunnerAddr uses for the SQL console: an
	// installation with no volume mounted for this must not fail to start
	// over a feature it never turned on.
	GameUploadDir string
	// GameUploadMaxFileBytes bounds one upload's total size. The pilot's own
	// numbers describe "1 GB-3 GB max"; the ceiling here is deliberately
	// above the number a person named, not equal to it — a script that grew
	// past what somebody guessed at design time should be a slow upload, not
	// a refusal an organiser has no way to raise themselves.
	GameUploadMaxFileBytes int64
	// GameUploadMaxDirBytes bounds every upload the volume holds together —
	// in progress, and complete ones waiting to be superseded or reclaimed.
	GameUploadMaxDirBytes int64
	// GameUploadTableMaxDirBytes is GameUploadMaxDirBytes's own counterpart
	// for the table builder's own per-table CSV data (internal/app's
	// tableDir): a second, independent gamefile.Store, on the same volume
	// as the dump's but never sharing its directory
	// (provisioning.Games.WithTableData's own doc explains why one Store
	// per directory matters). Two independent Stores each enforcing the
	// same MaxDirBytes would let the volume hold twice what an operator who
	// set GAME_UPLOAD_MAX_DIR_BYTES to the volume's own size meant to
	// allow — this field exists so the two ceilings are sized separately,
	// on purpose, rather than one silently doubling the other. 4 GiB unset:
	// a quarter of the dump's own 16 GiB default, since a table builder's
	// own CSV data is expected to run far smaller than a whole dump.
	GameUploadTableMaxDirBytes int64
	// GameUploadChunkBytes bounds one Append call, independent of the
	// upload's own size (internal/gamefile's own rule 12 reasoning).
	GameUploadChunkBytes int64
	// GameUploadAbandonedAfter is how long an upload may sit 'receiving'
	// with nothing appended to it before the janitor (internal/app/
	// background.go) aborts it and frees the disk. A day: long enough that
	// an organiser stepping away mid-upload for lunch does not lose their
	// place, short enough that a browser tab closed mid-upload does not hold
	// gigabytes indefinitely.
	GameUploadAbandonedAfter time.Duration
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
	if cfg.SessionMaxLifetime, err = durationEnv("SESSION_MAX_LIFETIME", 12*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.SessionMaxLifetime < minSessionMaxLifetime || cfg.SessionMaxLifetime > maxSessionMaxLifetime {
		return Config{}, fmt.Errorf("SESSION_MAX_LIFETIME: %s is outside [%s, %s]",
			cfg.SessionMaxLifetime, minSessionMaxLifetime, maxSessionMaxLifetime)
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
	// The same convention. Bounded because a ceiling nobody could reach is no
	// backstop at all.
	if cfg.MaxLoginAttemptsPerAccount, err = intEnv("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT", 0); err != nil {
		return Config{}, err
	}
	if cfg.MaxLoginAttemptsPerAccount > maxLoginAttemptsCeiling {
		return Config{}, fmt.Errorf("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT: %d is above the ceiling of %d",
			cfg.MaxLoginAttemptsPerAccount, maxLoginAttemptsCeiling)
	}
	// Both bounded: the concurrency is 64 MiB a slot, and the wait is how long
	// each request of a burst keeps a goroutine parked.
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

	// 30 unset, matching the Query Runner's own default for the same
	// variable (LoadRunner's QUERY_PER_MINUTE) — see the field's doc comment
	// for why the two must agree, including what zero means once it is set.
	if cfg.QueryPerMinute, err = intEnv("QUERY_PER_MINUTE", 30); err != nil {
		return Config{}, err
	}
	// Five seconds unset, the figure docs/ARCHITECTURE.md §8 names.
	if cfg.DeadlineGrace, err = durationEnv("DEADLINE_GRACE", 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.DeadlineGrace < 0 {
		return Config{}, fmt.Errorf("DEADLINE_GRACE cannot be negative, got %s", cfg.DeadlineGrace)
	}

	cfg.GameProvisionerDSN = os.Getenv("GAME_PROVISIONER_DSN")
	cfg.GameAuthorPassword = os.Getenv("GAME_AUTHOR_PASSWORD")
	// Checked at boot rather than at the first build, which would be an
	// organiser pressing "build" during preparation and being told the
	// deployment is misconfigured.
	if cfg.GameProvisionerDSN != "" && cfg.GameAuthorPassword == "" {
		return Config{}, fmt.Errorf(
			"GAME_AUTHOR_PASSWORD is required when GAME_PROVISIONER_DSN is set: " +
				"a game script runs as the game_author role and not as the provisioner")
	}
	// Thirty minutes unset — see the field's own doc for why that figure and
	// not the ten minutes the provisioning pool's own statement_timeout uses
	// (provisionStatementTimeout, internal/app/background.go): that one
	// bounds CREATE DATABASE and DROP DATABASE, not an organiser's own SQL.
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
	// A day unset: long enough for an organizer to pull reports and for a
	// participant's last-second answer to land safely, short enough that a
	// forgotten contest does not sit on a database indefinitely.
	if cfg.GameInstanceGraceMin, err = intEnv("GAME_INSTANCE_GRACE_MIN", 24*60); err != nil {
		return Config{}, err
	}
	if cfg.GameInstanceGraceMin < 0 {
		return Config{}, fmt.Errorf("GAME_INSTANCE_GRACE_MIN cannot be negative, got %d", cfg.GameInstanceGraceMin)
	}

	cfg.GameUploadDir = os.Getenv("GAME_UPLOAD_DIR")
	// 4 GiB unset: above the 1-3 GB the pilot itself named, on purpose (the
	// field's own doc).
	if cfg.GameUploadMaxFileBytes, err = int64Env("GAME_UPLOAD_MAX_FILE_BYTES", 4<<30); err != nil {
		return Config{}, err
	}
	if cfg.GameUploadMaxDirBytes, err = int64Env("GAME_UPLOAD_MAX_DIR_BYTES", 16<<30); err != nil {
		return Config{}, err
	}
	// 4 GiB unset — the field's own doc on why this does not fall back to
	// GameUploadMaxDirBytes's value.
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

	cfg.DefaultLocale = envOrDefault("DEFAULT_LOCALE", "en")
	if !languageTag.MatchString(cfg.DefaultLocale) {
		return Config{}, fmt.Errorf("DEFAULT_LOCALE: %q is not a language tag", cfg.DefaultLocale)
	}

	// Validation happens here so a typo fails the boot; the list is split
	// eagerly and re-validated by the resolver that consumes it.
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

// int64Env is intEnv for a quantity that is not a count of things but a
// number of bytes, where a deployment's own figure can be larger than a
// setting anybody would type as a count.
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
