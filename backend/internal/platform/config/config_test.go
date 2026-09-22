package config

import (
	"os"
	"path/filepath"
	"regexp"
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
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
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

// GameBuildTimeout bounds one run of an organiser's game script — see the
// field's own doc for why thirty minutes and not the provisioning pool's own
// ten.
func TestGameBuildTimeoutDefaultsToThirtyMinutes(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameBuildTimeout != 30*time.Minute {
		t.Errorf("GameBuildTimeout = %s, want 30m", cfg.GameBuildTimeout)
	}
}

func TestGameBuildTimeoutIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_BUILD_TIMEOUT", "2h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameBuildTimeout != 2*time.Hour {
		t.Errorf("GameBuildTimeout = %s, want 2h", cfg.GameBuildTimeout)
	}
}

func TestANonPositiveGameBuildTimeoutIsRejected(t *testing.T) {
	for _, value := range []string{"0", "-1h"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
			t.Setenv("GAME_BUILD_TIMEOUT", value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted GAME_BUILD_TIMEOUT=%s, want error", value)
			}
		})
	}
}

func TestCookieIsSecureOutsideDevelopment(t *testing.T) {
	// The dangerous default is the insecure one, so production must not have
	// to remember a flag to get it right.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))

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
	// "Without" has to be stated, not assumed. Load reads the real
	// environment, and GAME_AUTHOR_PASSWORD is set in any shell that can run
	// the game-cluster tests — `make test-game` exports it, and so does the
	// CI job now that it starts a game cluster of its own. Inherited, it made
	// this test fail for the one reason that is not a defect: the variable it
	// is asserting the absence of was present.
	t.Setenv("GAME_AUTHOR_PASSWORD", "")

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
	if cfg.GameUploadTableMaxDirBytes != 4<<30 {
		t.Errorf("GameUploadTableMaxDirBytes = %d, want 4 GiB", cfg.GameUploadTableMaxDirBytes)
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
	t.Setenv("GAME_UPLOAD_TABLE_MAX_DIR_BYTES", "536870912")
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
	if cfg.GameUploadTableMaxDirBytes != 512<<20 {
		t.Errorf("GameUploadTableMaxDirBytes = %d, want 512 MiB", cfg.GameUploadTableMaxDirBytes)
	}
	if cfg.GameUploadChunkBytes != 1<<20 {
		t.Errorf("GameUploadChunkBytes = %d, want 1 MiB", cfg.GameUploadChunkBytes)
	}
	if cfg.GameUploadAbandonedAfter != time.Hour {
		t.Errorf("GameUploadAbandonedAfter = %v, want 1h", cfg.GameUploadAbandonedAfter)
	}
}

// GameUploadMaxDirBytes and GameUploadTableMaxDirBytes are two independent
// ceilings — provisioning.Games.WithTableData's own doc explains why the
// table builder's CSV data lives in a second, independent gamefile.Store
// rather than sharing the dump's — and an operator who only ever set the
// first (because there used to be only one Store to size) must not find the
// second silently defaulting to the same number: that would let the two
// stores together claim twice the volume the first variable's own name
// promises. This is why the field has its own default (4 GiB, a quarter of
// the dump's 16 GiB) rather than falling back to GameUploadMaxDirBytes's
// value: a fixed, named default an operator can see and size against the
// volume, not a silent inherited one that changes if the dump's own limit
// does.
func TestGameUploadTableMaxDirBytesIsIndependentOfTheDumpsOwnLimit(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_MAX_DIR_BYTES", "1099511627776") // 1 TiB, nothing like the table default

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.GameUploadTableMaxDirBytes != 4<<30 {
		t.Errorf("GameUploadTableMaxDirBytes = %d, want its own 4 GiB default, unaffected by GAME_UPLOAD_MAX_DIR_BYTES", cfg.GameUploadTableMaxDirBytes)
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

// A zero GameUploadTableMaxDirBytes would let Games.WithTableData construct
// a second gamefile.Store that refuses every table upload outright, the
// identical reasoning TestGameUploadDirWithAZeroLimitIsRejectedAtStartup
// gives for the dump's own directory limit — checked here separately
// because the two are now two different fields or this refusal could not
// tell them apart.
func TestGameUploadDirWithAZeroTableLimitIsRejectedAtStartup(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_TABLE_MAX_DIR_BYTES", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted GAME_UPLOAD_DIR with a zero table dir size, want error")
	}
}

// The variables this file reads are the whole of what an operator can
// configure, and deploy/.env.example is where they are told so. Neither of
// those facts reaches the running process on its own: the compose file has to
// pass each one into the api container, and a variable it does not pass is a
// variable an operator sets, restarts for, and never sees take effect — with
// no error and no log line, because from the process's side it was simply
// never set. That is how PUBLIC_ORIGINS came to be documented, settable, and
// dead: an operator whose interface and API answer on different names filled
// it in and still got 403 on every chunk upload.
//
// So the vocabulary this package declares is checked against the deployment
// that has to carry it, in the same way cmd/apicontract's own test checks the
// error codes against the file the interface reads. Only variables
// .env.example actually documents are required: a knob nobody is told about
// is a knob nobody sets (SHUTDOWN_TIMEOUT is the one such today), and a
// variable compose passes that this file does not read belongs to another
// service in the same file.
func TestEveryDocumentedVariableReachesTheAPIContainer(t *testing.T) {
	documented := documentedVariables(t)
	passed := apiServiceEnvironment(t)

	for _, name := range configVariables(t) {
		if !documented[name] {
			continue
		}
		if !passed[name] {
			t.Errorf("%s is read by config.Load and documented in deploy/.env.example, but "+
				"deploy/docker-compose.yml never passes it to the api service — an operator "+
				"setting it gets no effect and no error", name)
		}
	}
}

// repoFile reads one file from the repository root, four levels above this
// package's own directory.
func repoFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// configVariables are the environment variables config.go names, read out of
// its own source rather than listed here: a second, hand-typed copy of the
// vocabulary is exactly the drift the test above exists to catch (the same
// reasoning frontend/lib/i18n/dictionary.test.ts gives for reading the
// generated contract instead of AUDIT_ACTIONS).
func configVariables(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	var names []string
	for _, match := range envNamePattern.FindAllStringSubmatch(string(source), -1) {
		names = append(names, match[1])
	}
	if len(names) < 10 {
		t.Fatalf("found only %d variables in config.go (%v); the pattern has stopped matching", len(names), names)
	}
	return names
}

// A quoted SCREAMING_SNAKE_CASE literal in config.go is an environment
// variable's name and nothing else — there is no other kind of constant
// written that way in this file.
var envNamePattern = regexp.MustCompile(`"([A-Z][A-Z0-9_]{2,})"`)

// documentedVariables are the assignments deploy/.env.example carries, which
// is what an operator reads as "this is what you may set".
func documentedVariables(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	for _, line := range strings.Split(repoFile(t, "deploy/.env.example"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, "="); ok {
			found[strings.TrimSpace(name)] = true
		}
	}
	if len(found) < 10 {
		t.Fatalf("deploy/.env.example parsed as %d assignments; the format has changed", len(found))
	}
	return found
}

// apiServiceEnvironment are the keys the compose file's `api` service passes
// in. Parsed by indentation rather than with a YAML library: the shape being
// read is two known levels deep in a file this repository owns, and adding a
// dependency to a test that guards a deployment file is a worse trade than
// twenty lines that fail loudly when the shape changes (the guard below).
func apiServiceEnvironment(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	var inAPI, inEnv bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "  api:"):
			inAPI = true
		case inAPI && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "":
			inAPI, inEnv = false, false // the next service begins
		case inAPI && strings.HasPrefix(line, "    environment:"):
			inEnv = true
		case inAPI && inEnv && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") &&
			strings.TrimSpace(line) != "":
			inEnv = false // the next key of the api service
		case inAPI && inEnv && strings.HasPrefix(line, "      "):
			if name, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && !strings.HasPrefix(name, "#") {
				keys[name] = true
			}
		}
	}
	if len(keys) < 10 {
		t.Fatalf("the api service parsed as %d environment keys; docker-compose.yml's shape has changed", len(keys))
	}
	return keys
}

func TestPasswordHashConcurrencyIsLeftToThePasswordPackageByDefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.PasswordHashConcurrency != 0 {
		t.Errorf("PasswordHashConcurrency = %d, want 0 (the password package's own default)", cfg.PasswordHashConcurrency)
	}
	if cfg.PasswordHashMaxWait != 2*time.Second {
		t.Errorf("PasswordHashMaxWait = %v, want 2s", cfg.PasswordHashMaxWait)
	}
}

func TestPasswordHashingIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("PASSWORD_HASH_CONCURRENCY", "4")
	t.Setenv("PASSWORD_HASH_MAX_WAIT", "500ms")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.PasswordHashConcurrency != 4 {
		t.Errorf("PasswordHashConcurrency = %d, want 4", cfg.PasswordHashConcurrency)
	}
	if cfg.PasswordHashMaxWait != 500*time.Millisecond {
		t.Errorf("PasswordHashMaxWait = %v, want 500ms", cfg.PasswordHashMaxWait)
	}
}

func TestPasswordHashingOutsideItsBoundsIsRejected(t *testing.T) {
	// Each slot is 64 MiB, so the concurrency is a memory figure and has a
	// ceiling; a wait of zero would refuse every sign-in that meets another
	// one, and a long wait parks a goroutine per request of a flood.
	for name, env := range map[string][2]string{
		"concurrency above the ceiling": {"PASSWORD_HASH_CONCURRENCY", "65"},
		"negative concurrency":          {"PASSWORD_HASH_CONCURRENCY", "-1"},
		"zero wait":                     {"PASSWORD_HASH_MAX_WAIT", "0s"},
		"negative wait":                 {"PASSWORD_HASH_MAX_WAIT", "-1s"},
		"wait above the ceiling":        {"PASSWORD_HASH_MAX_WAIT", "31s"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
			t.Setenv(env[0], env[1])

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted %s=%s, want error", env[0], env[1])
			} else if !strings.Contains(err.Error(), env[0]) {
				t.Errorf("error %q does not name %s", err, env[0])
			}
		})
	}
}

func TestTheAnswerRateDefaultsToSixAMinute(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.AnswerRatePerMinute != 6 {
		t.Errorf("AnswerRatePerMinute = %d, want 6", cfg.AnswerRatePerMinute)
	}
}

func TestTheAnswerRateIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ANSWER_RATE_PER_MINUTE", "12")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.AnswerRatePerMinute != 12 {
		t.Errorf("AnswerRatePerMinute = %d, want 12", cfg.AnswerRatePerMinute)
	}

	// Zero is not "unlimited" here: an answer budget nobody can turn off is the
	// point of having one, and above sixty a minute it no longer slows a
	// script walking a candidate list.
	for _, value := range []string{"0", "61", "-1", "many"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
			t.Setenv("ANSWER_RATE_PER_MINUTE", value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted ANSWER_RATE_PER_MINUTE=%s, want error", value)
			} else if !strings.Contains(err.Error(), "ANSWER_RATE_PER_MINUTE") {
				t.Errorf("error %q does not name ANSWER_RATE_PER_MINUTE", err)
			}
		})
	}
}

func TestTheAccountWideLoginCeilingIsLeftToAuthenticationByDefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.MaxLoginAttemptsPerAccount != 0 {
		t.Errorf("MaxLoginAttemptsPerAccount = %d, want 0 (the auth package's own default)", cfg.MaxLoginAttemptsPerAccount)
	}
}

func TestTheAccountWideLoginCeilingIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT", "250")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.MaxLoginAttemptsPerAccount != 250 {
		t.Errorf("MaxLoginAttemptsPerAccount = %d, want 250", cfg.MaxLoginAttemptsPerAccount)
	}

	for _, raw := range []string{"-1", "100001"} {
		t.Setenv("MAX_LOGIN_ATTEMPTS_PER_ACCOUNT", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted MAX_LOGIN_ATTEMPTS_PER_ACCOUNT=%s, want error", raw)
		}
	}
}

func TestCoreDBPoolMaxIsLeftToThePoolsOwnDefaultByDefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.CoreDBPoolMax != 0 {
		t.Errorf("CoreDBPoolMax = %d, want 0 (the pool's own default)", cfg.CoreDBPoolMax)
	}
}

func TestCoreDBPoolMaxIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("CORE_DB_POOL_MAX", "40")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.CoreDBPoolMax != 40 {
		t.Errorf("CoreDBPoolMax = %d, want 40", cfg.CoreDBPoolMax)
	}

	for _, raw := range []string{"-1", "101"} {
		t.Setenv("CORE_DB_POOL_MAX", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted CORE_DB_POOL_MAX=%s, want error", raw)
		}
	}
}

// Each bound holds its own value inside a sane range, and neither notices
// that the two together ask for more connections than exist: a download holds
// its connection for as long as the reader takes, so ten of them against a
// pool of five is the whole pool held by people who may be slow on purpose,
// and sign-in queueing behind them.
func TestExportConcurrencyIsCheckedAgainstThePoolItIsAShareOf(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("CORE_DB_POOL_MAX", "5")

	t.Setenv("EXPORT_CONCURRENCY", "10")
	if _, err := Load(); err == nil {
		t.Error("Load() accepted EXPORT_CONCURRENCY=10 against CORE_DB_POOL_MAX=5, want error")
	}

	// Two fifths of the pool is the share the ceiling itself is derived
	// from, and it is allowed.
	t.Setenv("EXPORT_CONCURRENCY", "2")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ExportConcurrency != 2 {
		t.Errorf("ExportConcurrency = %d, want 2", cfg.ExportConcurrency)
	}

	// A pool left to its own default is the case the standing ceiling was
	// already sized against, so the cross-check has nothing to say about it.
	t.Setenv("CORE_DB_POOL_MAX", "")
	t.Setenv("EXPORT_CONCURRENCY", "10")
	if _, err := Load(); err != nil {
		t.Errorf("Load() = %v, want the default pool to accept EXPORT_CONCURRENCY=10", err)
	}
}

func TestSessionMaximumLifetimeDefaultsToAWorkingDay(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.SessionMaxLifetime != 12*time.Hour {
		t.Errorf("SessionMaxLifetime = %v, want 12h", cfg.SessionMaxLifetime)
	}
}

func TestSessionMaximumLifetimeIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("SESSION_MAX_LIFETIME", "8h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.SessionMaxLifetime != 8*time.Hour {
		t.Errorf("SessionMaxLifetime = %v, want 8h", cfg.SessionMaxLifetime)
	}

	// Below a few minutes nobody finishes signing in and doing anything; past
	// a week the limit stops being one.
	for _, raw := range []string{"0s", "-1h", "4m", "169h"} {
		t.Setenv("SESSION_MAX_LIFETIME", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted SESSION_MAX_LIFETIME=%s, want error", raw)
		}
	}
}

func TestTheAccountCacheLifetimeDefaultsToAFewSeconds(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.SessionAccountCacheTTL != 5*time.Second {
		t.Errorf("SessionAccountCacheTTL = %v, want 5s", cfg.SessionAccountCacheTTL)
	}
}

func TestTheAccountCacheLifetimeIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	// Zero turns the cache off: every request reads the account.
	for raw, want := range map[string]time.Duration{"0s": 0, "2s": 2 * time.Second, "30s": 30 * time.Second} {
		t.Setenv("SESSION_ACCOUNT_CACHE_TTL", raw)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() with SESSION_ACCOUNT_CACHE_TTL=%s returned error: %v", raw, err)
		}
		if cfg.SessionAccountCacheTTL != want {
			t.Errorf("SESSION_ACCOUNT_CACHE_TTL=%s gave %v, want %v", raw, cfg.SessionAccountCacheTTL, want)
		}
	}

	// It is how long a change nothing could announce takes to apply, a
	// block made by hand included; past half a minute that stops being soon.
	for _, raw := range []string{"-1s", "31s", "5m", "soon"} {
		t.Setenv("SESSION_ACCOUNT_CACHE_TTL", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted SESSION_ACCOUNT_CACHE_TTL=%s, want error", raw)
		}
	}
}

func TestTheDeviceCookieSecretIsRequiredOutsideDevelopment(t *testing.T) {
	// Without it every device cookie would be signed with a key nobody chose
	// — or a fresh one per restart, silently distrusting every browser.
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DEVICE_COOKIE_SECRET") {
		t.Fatalf("Load() without DEVICE_COOKIE_SECRET in production = %v, want an error naming it", err)
	}

	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 31))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DEVICE_COOKIE_SECRET") {
		t.Errorf("Load() with a 31-byte DEVICE_COOKIE_SECRET = %v, want an error naming it", err)
	}

	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with a 32-byte secret returned error: %v", err)
	}
	if string(cfg.DeviceCookieSecret) != strings.Repeat("s", 32) {
		t.Error("DeviceCookieSecret is not the configured value")
	}
}

func TestADevelopmentStackGeneratesItsOwnDeviceCookieSecret(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "development")

	first, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	second, _ := Load()

	if len(first.DeviceCookieSecret) < 32 {
		t.Errorf("generated secret is %d bytes, want at least 32", len(first.DeviceCookieSecret))
	}
	if string(first.DeviceCookieSecret) == string(second.DeviceCookieSecret) {
		t.Error("two loads generated the same secret: it is not random")
	}
}

func TestDeviceCookieSettingsHaveDefaultsAndBounds(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DeviceCookieTTL != 30*24*time.Hour {
		t.Errorf("DeviceCookieTTL = %v, want 720h", cfg.DeviceCookieTTL)
	}
	if cfg.MaxLoginAttemptsPerDevice != 0 {
		t.Errorf("MaxLoginAttemptsPerDevice = %d, want 0 (the auth package's default)", cfg.MaxLoginAttemptsPerDevice)
	}
	if cfg.MaxTrustedLoginAttemptsPerAccount != 0 {
		t.Errorf("MaxTrustedLoginAttemptsPerAccount = %d, want 0 (the auth package's default)", cfg.MaxTrustedLoginAttemptsPerAccount)
	}

	for env, bad := range map[string][]string{
		"DEVICE_COOKIE_TTL":                      {"0s", "59m", "2161h"},
		"MAX_LOGIN_ATTEMPTS_PER_DEVICE":          {"-1", "1001"},
		"MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT": {"-1", "1001"},
	} {
		for _, raw := range bad {
			t.Run(env+"="+raw, func(t *testing.T) {
				t.Setenv(env, raw)
				if _, err := Load(); err == nil {
					t.Errorf("Load() accepted %s=%s", env, raw)
				}
			})
		}
	}
}

func TestTheTrustedSignInBudgetIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("MAX_TRUSTED_LOGIN_ATTEMPTS_PER_ACCOUNT", "40")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.MaxTrustedLoginAttemptsPerAccount != 40 {
		t.Errorf("MaxTrustedLoginAttemptsPerAccount = %d, want 40", cfg.MaxTrustedLoginAttemptsPerAccount)
	}
}

// The Query Runner runs whatever database and policy a request names, so the
// Core API's calls to it must carry the shared token wherever the console is
// on. A production API that starts without one would find out at the first
// participant's query, from a refusal; refusing the boot names the cause.
func TestTheQueryRunnerTokenIsRequiredOutsideDevelopmentWhenTheConsoleIsOn(t *testing.T) {
	t.Setenv("QUERY_RUNNER_TOKEN", "") // not inherited from the shell
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
	t.Setenv("QUERY_RUNNER_ADDR", "queryrunner:9100")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
		t.Fatalf("Load() without QUERY_RUNNER_TOKEN in production = %v, want an error naming it", err)
	}

	short := strings.Repeat("t", 31)
	t.Setenv("QUERY_RUNNER_TOKEN", short)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
		t.Fatalf("Load() with a 31-byte QUERY_RUNNER_TOKEN = %v, want an error naming it", err)
	}
	if strings.Contains(err.Error(), short) {
		t.Fatalf("the refusal repeats the token: %q", err.Error())
	}

	token := strings.Repeat("t", 32)
	t.Setenv("QUERY_RUNNER_TOKEN", token)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with a 32-byte token returned error: %v", err)
	}
	if cfg.QueryRunnerToken != token {
		t.Error("QueryRunnerToken is not the configured value")
	}
}

// The device cookie secret signs what a browser presents to sign in without
// the address limit; the Query Runner token is sent to another service on
// every query. One value in both places makes a leak of either the other, so
// the API refuses to start with them equal — and says so without repeating it.
func TestTheDeviceCookieSecretMustNotBeTheQueryRunnerToken(t *testing.T) {
	shared := strings.Repeat("x", 40)
	for _, env := range []string{"production", "development"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
			t.Setenv("ENV", env)
			t.Setenv("QUERY_RUNNER_ADDR", "queryrunner:9100")
			t.Setenv("DEVICE_COOKIE_SECRET", shared)
			t.Setenv("QUERY_RUNNER_TOKEN", shared)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "DEVICE_COOKIE_SECRET") ||
				!strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
				t.Fatalf("Load() with one value for both = %v, want an error naming both", err)
			}
			if strings.Contains(err.Error(), shared) {
				t.Fatalf("the refusal repeats the secret: %q", err.Error())
			}
		})
	}
}

// Without an address the API never dials the runner, so there is nothing for
// a token to protect and nothing to refuse.
func TestTheQueryRunnerTokenIsNotRequiredWithoutAConsole(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
	t.Setenv("QUERY_RUNNER_ADDR", "")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() without a console refused a missing token: %v", err)
	}
}

// Development may run without a token, but a token that is set is held to the
// same bounds everywhere: a short one set on a laptop is the one copied into
// production.
func TestADevelopmentAPIMayOmitTheQueryRunnerTokenButNotShortenIt(t *testing.T) {
	t.Setenv("QUERY_RUNNER_TOKEN", "") // not inherited from the shell
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "development")
	t.Setenv("QUERY_RUNNER_ADDR", "localhost:9100")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() in development without a token: %v", err)
	}
	if cfg.QueryRunnerToken != "" {
		t.Errorf("QueryRunnerToken = %q, want empty", cfg.QueryRunnerToken)
	}

	for name, raw := range map[string]string{
		"too short":      strings.Repeat("t", 31),
		"too long":       strings.Repeat("t", 513),
		"a space inside": strings.Repeat("t", 20) + " " + strings.Repeat("t", 20),
		"a control char": strings.Repeat("t", 40) + "\n",
		"non-ascii":      strings.Repeat("t", 40) + "é",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("QUERY_RUNNER_TOKEN", raw)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
				t.Fatalf("Load() = %v, want an error naming QUERY_RUNNER_TOKEN", err)
			}
			if strings.Contains(err.Error(), raw) {
				t.Fatalf("the refusal repeats the token: %q", err.Error())
			}
		})
	}
}

// TRUSTED_PROXIES decides whose X-Forwarded-For the API believes, and so the
// per-address login throttle, a contest's network restriction and the audit
// trail. The compose file pins the two containers that front a browser and
// defaults to exactly those; an operator who follows `cp .env.example .env`
// must get the same, not a range that trusts every container on the network.
func TestTheExampleTrustsOnlyThePinnedProxies(t *testing.T) {
	compose := repoFile(t, "deploy/docker-compose.yml")

	pinned := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^\s+ipv4_address:\s*(\S+)\s*$`).FindAllStringSubmatch(compose, -1) {
		pinned[match[1]] = true
	}
	if len(pinned) == 0 {
		t.Fatal("docker-compose.yml pins no address; the shape has changed")
	}

	fallback := regexp.MustCompile(`TRUSTED_PROXIES:\s*\$\{TRUSTED_PROXIES:-([^}]*)\}`).FindStringSubmatch(compose)
	if fallback == nil {
		t.Fatal("docker-compose.yml no longer defaults TRUSTED_PROXIES; the shape has changed")
	}

	var example string
	for _, line := range strings.Split(repoFile(t, "deploy/.env.example"), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "TRUSTED_PROXIES="); ok {
			example = value
		}
	}

	for name, list := range map[string]string{"compose default": fallback[1], ".env.example": example} {
		entries := strings.Split(list, ",")
		for _, entry := range entries {
			entry = strings.TrimSpace(entry)
			address, bits, _ := strings.Cut(entry, "/")
			if bits != "" && bits != "32" {
				t.Errorf("%s trusts %s, a range rather than one host", name, entry)
			}
			if !pinned[address] {
				t.Errorf("%s trusts %s, which is not an address docker-compose.yml pins", name, entry)
			}
		}
	}
	if example != fallback[1] {
		t.Errorf(".env.example sets TRUSTED_PROXIES=%s, but the compose default is %s", example, fallback[1])
	}
}

// deploy/.env.example marks every value an operator must choose with
// "change-me". A deployment started from a copied file without replacing them
// runs on credentials anybody who has read the repository knows, and nothing
// else in the system notices. Outside development each variable the API reads
// a credential from refuses one, naming the variable and never the value.
func TestAPlaceholderCredentialIsRefusedOutsideDevelopment(t *testing.T) {
	production := func(t *testing.T) {
		t.Helper()
		t.Setenv("ENV", "production")
		t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
		t.Setenv("REDIS_ADDR", "")
		t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
		t.Setenv("QUERY_RUNNER_ADDR", "queryrunner:9100")
		t.Setenv("QUERY_RUNNER_TOKEN", strings.Repeat("t", 32))
		t.Setenv("GAME_PROVISIONER_DSN", "")
		t.Setenv("GAME_AUTHOR_PASSWORD", "")
	}

	for name, value := range map[string]string{
		"DEVICE_COOKIE_SECRET": "change-me-to-at-least-32-random-bytes",
		"QUERY_RUNNER_TOKEN":   "CHANGE-ME-to-the-output-of-openssl-rand",
		"CORE_DB_DSN":          "postgres://dbcontest:change-me-before-first-run@pg-core:5432/core",
		"REDIS_ADDR":           "redis://:Change-Me-Before-First-Run@redis:6379/0",
		"GAME_PROVISIONER_DSN": "postgres://game:change-me-before-first-run@pg-game:5432/game",
		"GAME_AUTHOR_PASSWORD": "change-me-before-first-run",
	} {
		t.Run(name, func(t *testing.T) {
			production(t)
			if name == "GAME_AUTHOR_PASSWORD" || name == "GAME_PROVISIONER_DSN" {
				// The two are required together.
				t.Setenv("GAME_PROVISIONER_DSN", "postgres://game:real@pg-game:5432/game")
				t.Setenv("GAME_AUTHOR_PASSWORD", "a-real-author-password")
			}
			if _, err := Load(); err != nil {
				t.Fatalf("the baseline production configuration was refused: %v", err)
			}

			t.Setenv(name, value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("Load() with a placeholder %s = %v, want an error naming it", name, err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "change-me") {
				t.Fatalf("the refusal repeats the value: %q", err.Error())
			}

			// A development stack may keep the example's values.
			t.Setenv("ENV", "development")
			if _, err := Load(); err != nil {
				t.Fatalf("development refused a placeholder %s: %v", name, err)
			}
		})
	}
}
