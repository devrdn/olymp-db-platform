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
	//
	// The variable is cleared rather than assumed absent: CI exports it for
	// the whole job, and a test that reads the ambient environment passes or
	// fails by accident of where it runs.
	t.Setenv("CORE_DB_DSN", "")
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

func TestSessionLifetimeDefaultsToAWorkingDay(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.SessionTTL != 12*time.Hour {
		t.Errorf("SessionTTL = %v, want 12h", cfg.SessionTTL)
	}
}

func TestSessionLifetimeIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("SESSION_TTL", "4h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.SessionTTL != 4*time.Hour {
		t.Errorf("SessionTTL = %v, want 4h", cfg.SessionTTL)
	}
}

func TestDeadlineGraceDefaultsToFiveSeconds(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DeadlineGrace != 5*time.Second {
		t.Errorf("DeadlineGrace = %v, want 5s", cfg.DeadlineGrace)
	}
}

func TestDeadlineGraceIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("DEADLINE_GRACE", "2s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DeadlineGrace != 2*time.Second {
		t.Errorf("DeadlineGrace = %v, want 2s", cfg.DeadlineGrace)
	}
}

func TestNegativeDeadlineGraceIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("DEADLINE_GRACE", "-1s")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a negative DEADLINE_GRACE, want error")
	}
}

// GameInstanceGraceMin is what a contest's own grace_period_min defers to
// when it is left at zero (provisioning.effectiveGrace's convention, the same
// one QueryPerMinute documents above): an installation that never configures
// this still keeps every participant's database for a day after their
// contest finishes, rather than reclaiming on the very first tick after the
// feature ships.
func TestGameInstanceGraceDefaultsToADay(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.GameInstanceGraceMin != 24*60 {
		t.Errorf("GameInstanceGraceMin = %d, want 1440 (24h)", cfg.GameInstanceGraceMin)
	}
}

func TestGameInstanceGraceIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_INSTANCE_GRACE_MIN", "30")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.GameInstanceGraceMin != 30 {
		t.Errorf("GameInstanceGraceMin = %d, want 30", cfg.GameInstanceGraceMin)
	}
}

func TestNegativeGameInstanceGraceIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_INSTANCE_GRACE_MIN", "-1")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a negative GAME_INSTANCE_GRACE_MIN, want error")
	}
}

func TestCookieIsSecureOutsideDevelopment(t *testing.T) {
	// The dangerous default is the insecure one, so production must not have
	// to remember a flag to get it right.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if !cfg.CookieSecure {
		t.Error("CookieSecure = false in production; the session cookie would travel unprotected")
	}
}

func TestCookieIsNotSecureInDevelopment(t *testing.T) {
	// A local stack has no certificate, and a browser silently drops a Secure
	// cookie over plain HTTP — nobody could sign in.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.CookieSecure {
		t.Error("CookieSecure = true in development; local sign-in would be impossible")
	}
}

func TestCookieSecurityCanBeOverridden(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "development")
	t.Setenv("COOKIE_SECURE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if !cfg.CookieSecure {
		t.Error("the explicit override was ignored")
	}
}

func TestTrustedProxiesDefaultToNone(t *testing.T) {
	// Trusting nobody is the safe default: forwarded headers stay ignored.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty", cfg.TrustedProxies)
	}
}

func TestTrustedProxiesParseCommaSeparatedList(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("TRUSTED_PROXIES", "172.28.0.0/16, 10.0.0.1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "172.28.0.0/16" || cfg.TrustedProxies[1] != "10.0.0.1" {
		t.Errorf("TrustedProxies = %v, want the two entries", cfg.TrustedProxies)
	}
}

func TestTrustedProxiesRejectGarbageAtStartup(t *testing.T) {
	// A typo must fail the boot, not silently produce a resolver that trusts
	// nobody and reintroduces the shared-throttle bug behind the proxy.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("TRUSTED_PROXIES", "not-a-cidr")

	if _, err := Load(); err == nil {
		t.Error("Load() accepted an unparseable TRUSTED_PROXIES entry")
	}
}

func TestDefaultLocaleFallsBackToEnglish(t *testing.T) {
	// The last resort when a request expresses no preference and no contest
	// context has narrowed it down. English because the game database is
	// English, so it is the one language every installation certainly has.
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DefaultLocale != "en" {
		t.Errorf("DefaultLocale = %q, want en", cfg.DefaultLocale)
	}
}

func TestDefaultLocaleIsConfigurable(t *testing.T) {
	// An installation that runs entirely in Romanian should not have to see
	// English first; the code is data, so no rebuild is involved.
	setRequired(t)
	t.Setenv("DEFAULT_LOCALE", "ro")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DefaultLocale != "ro" {
		t.Errorf("DefaultLocale = %q, want ro", cfg.DefaultLocale)
	}
}

func TestDefaultLocaleRejectsSomethingThatIsNotALanguageTag(t *testing.T) {
	// Catching a typo at boot beats every participant silently getting the
	// wrong fallback.
	setRequired(t)
	t.Setenv("DEFAULT_LOCALE", "not a tag!")

	if _, err := Load(); err == nil {
		t.Error("Load() accepted a malformed DEFAULT_LOCALE")
	}
}

// A deployment that provisions game databases must also say what the
// game-script role authenticates with.
//
// The two travel together because the second is what stops an organiser's
// uploaded SQL running with the first one's privileges (gamedb.RoleAuthor).
// Caught at boot rather than at the first build, which would be an organiser
// pressing "build" during preparation and being told the deployment is
// misconfigured.
func TestAProvisionerWithoutTheGameAuthorPasswordIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_PROVISIONER_DSN", "postgres://provisioner:pass@pg-game:5432/game")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a provisioner with no GAME_AUTHOR_PASSWORD, want error")
	}
	if !strings.Contains(err.Error(), "GAME_AUTHOR_PASSWORD") {
		t.Fatalf("Load() failed with %v, which does not name the missing variable", err)
	}
}

// And with it, both reach the composition root.
func TestTheGameAuthorPasswordIsRead(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_PROVISIONER_DSN", "postgres://provisioner:pass@pg-game:5432/game")
	t.Setenv("GAME_AUTHOR_PASSWORD", "an-author-password")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameAuthorPassword != "an-author-password" {
		t.Errorf("GameAuthorPassword = %q, want the value from the environment", cfg.GameAuthorPassword)
	}
}

// A deployment with no game cluster needs neither, and must still start: the
// game circuit is optional and registration does not depend on it.
func TestNeitherGameCredentialIsRequiredWithoutAProvisioner(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() refused a deployment with no game cluster: %v", err)
	}
}

// The byte budget the game pool is sized against. A default rather than
// "unlimited", because unlimited is the state where an open contest's roster —
// written by whoever self-enrols — is a lever on the disk every olympiad on
// the cluster shares.
func TestTheGameClusterHasAByteBudgetByDefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ClusterMaxBytes != 64<<30 {
		t.Errorf("ClusterMaxBytes = %d, want 64 GiB", cfg.ClusterMaxBytes)
	}
}

// And it is sized by the operator, in bytes, because only they know the volume
// underneath the cluster. Past a 32-bit integer, which is the whole reason it
// is not read as an ordinary count.
func TestTheGameClusterByteBudgetIsConfigurableBeyondFourGibibytes(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_CLUSTER_MAX_BYTES", "1099511627776")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ClusterMaxBytes != 1<<40 {
		t.Errorf("ClusterMaxBytes = %d, want 1 TiB", cfg.ClusterMaxBytes)
	}
}

func TestANegativeGameClusterByteBudgetIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_CLUSTER_MAX_BYTES", "-1")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a negative GAME_CLUSTER_MAX_BYTES, want error")
	}
}

// File uploads are off by default — GameUploadDir empty is exactly what an
// installation with no upload volume mounted needs, the same convention
// QueryRunnerAddr uses to turn the console off — and the size limits still
// come back with real defaults so a deployment that only sets GAME_UPLOAD_DIR
// is not left with a zero-byte ceiling.
func TestGameUploadsAreOffByDefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameUploadDir != "" {
		t.Errorf("GameUploadDir = %q, want empty (uploads off) by default", cfg.GameUploadDir)
	}
	if cfg.GameUploadMaxFileBytes != 4<<30 {
		t.Errorf("GameUploadMaxFileBytes = %d, want 4 GiB", cfg.GameUploadMaxFileBytes)
	}
	if cfg.GameUploadMaxDirBytes != 16<<30 {
		t.Errorf("GameUploadMaxDirBytes = %d, want 16 GiB", cfg.GameUploadMaxDirBytes)
	}
	if cfg.GameUploadChunkBytes != 8<<20 {
		t.Errorf("GameUploadChunkBytes = %d, want 8 MiB", cfg.GameUploadChunkBytes)
	}
	if cfg.GameUploadAbandonedAfter != 24*time.Hour {
		t.Errorf("GameUploadAbandonedAfter = %v, want 24h", cfg.GameUploadAbandonedAfter)
	}
}

// The pilot itself named "1 GB-3 GB max" for one upload; the compiled
// default has to sit above that named ceiling, not equal to it, or the
// first organiser to approach what somebody guessed at design time is
// refused for a reason they have no way to raise themselves.
func TestGameUploadMaxFileBytesDefaultsAboveThePilotsOwnCeiling(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	const threeGB = 3 * 1_000_000_000
	if cfg.GameUploadMaxFileBytes <= threeGB {
		t.Errorf("GameUploadMaxFileBytes = %d, want more than the named 3 GB ceiling", cfg.GameUploadMaxFileBytes)
	}
}

func TestGameUploadsAreConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_MAX_FILE_BYTES", "1073741824")
	t.Setenv("GAME_UPLOAD_MAX_DIR_BYTES", "2147483648")
	t.Setenv("GAME_UPLOAD_CHUNK_BYTES", "1048576")
	t.Setenv("GAME_UPLOAD_ABANDONED_AFTER", "1h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameUploadDir != "/var/lib/dbcontest/uploads" {
		t.Errorf("GameUploadDir = %q", cfg.GameUploadDir)
	}
	if cfg.GameUploadMaxFileBytes != 1<<30 {
		t.Errorf("GameUploadMaxFileBytes = %d, want 1 GiB", cfg.GameUploadMaxFileBytes)
	}
	if cfg.GameUploadMaxDirBytes != 2<<30 {
		t.Errorf("GameUploadMaxDirBytes = %d, want 2 GiB", cfg.GameUploadMaxDirBytes)
	}
	if cfg.GameUploadChunkBytes != 1<<20 {
		t.Errorf("GameUploadChunkBytes = %d, want 1 MiB", cfg.GameUploadChunkBytes)
	}
	if cfg.GameUploadAbandonedAfter != time.Hour {
		t.Errorf("GameUploadAbandonedAfter = %v, want 1h", cfg.GameUploadAbandonedAfter)
	}
}

func TestNegativeGameUploadAbandonedAfterIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_ABANDONED_AFTER", "-1h")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a negative GAME_UPLOAD_ABANDONED_AFTER, want error")
	}
}

// A zero size limit configured alongside a real upload directory would let
// Games.WithUploads construct a gamefile.Store that refuses every upload
// outright (its own NewStore requires positive limits) — refused here, at
// boot, rather than surfacing later as every organiser's upload failing.
func TestGameUploadDirWithAZeroLimitIsRejectedAtStartup(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_CHUNK_BYTES", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted GAME_UPLOAD_DIR with a zero chunk size, want error")
	}
}
