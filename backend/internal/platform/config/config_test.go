package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
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
	// Cleared rather than assumed absent: CI exports it for the whole job.
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
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("TRUSTED_PROXIES", "not-a-cidr")

	if _, err := Load(); err == nil {
		t.Error("Load() accepted an unparseable TRUSTED_PROXIES entry")
	}
}

func TestDefaultLocaleFallsBackToEnglish(t *testing.T) {
	// English, because the game database is English.
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
	setRequired(t)
	t.Setenv("DEFAULT_LOCALE", "not a tag!")

	if _, err := Load(); err == nil {
		t.Error("Load() accepted a malformed DEFAULT_LOCALE")
	}
}

func TestAProvisionerWithoutTheGameAuthorPasswordIsRejected(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_PROVISIONER_DSN", "postgres://provisioner:pass@pg-game:5432/game")
	// Cleared: `make test-game` and CI export it.
	t.Setenv("GAME_AUTHOR_PASSWORD", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a provisioner with no GAME_AUTHOR_PASSWORD, want error")
	}
	if !strings.Contains(err.Error(), "GAME_AUTHOR_PASSWORD") {
		t.Fatalf("Load() failed with %v, which does not name the missing variable", err)
	}
}

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

func TestNeitherGameCredentialIsRequiredWithoutAProvisioner(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() refused a deployment with no game cluster: %v", err)
	}
}

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

func TestGameUploadDirWithAZeroLimitIsRejectedAtStartup(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_CHUNK_BYTES", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted GAME_UPLOAD_DIR with a zero chunk size, want error")
	}
}

func TestGameUploadDirWithAZeroTableLimitIsRejectedAtStartup(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("GAME_UPLOAD_DIR", "/var/lib/dbcontest/uploads")
	t.Setenv("GAME_UPLOAD_TABLE_MAX_DIR_BYTES", "0")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted GAME_UPLOAD_DIR with a zero table dir size, want error")
	}
}

func TestTheCoverDirectoryHasADefault(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.CoverDir != "/var/lib/dbcontest/covers" {
		t.Errorf("CoverDir = %q, want /var/lib/dbcontest/covers", cfg.CoverDir)
	}
}

func TestTheCoverDirectoryIsConfigurable(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("COVER_DIR", "/srv/dbcontest/covers")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.CoverDir != "/srv/dbcontest/covers" {
		t.Errorf("CoverDir = %q, want /srv/dbcontest/covers", cfg.CoverDir)
	}
}

// A variable compose does not pass into the api container is silently never
// set. Only variables deploy/.env.example documents are required to reach it.
func TestEveryDocumentedVariableReachesTheAPIContainer(t *testing.T) {
	assertDocumentedVariablesReach(t, "api", sourceVariables(t, "config.go"))
}

// The same for the Query Runner, whose variables runner.go names. The token is
// named explicitly: the runner reads it through queryRunnerToken in config.go,
// shared with the API, so no pattern over runner.go finds it.
func TestEveryDocumentedRunnerVariableReachesTheQueryRunnerContainer(t *testing.T) {
	assertDocumentedVariablesReach(t, "queryrunner", append(sourceVariables(t, "runner.go"), "QUERY_RUNNER_TOKEN"))
}

func assertDocumentedVariablesReach(t *testing.T, service string, read []string) {
	t.Helper()
	documented := documentedVariables(t)
	passed := serviceEnvironment(t, service, 10)

	for _, name := range read {
		if !documented[name] {
			continue
		}
		if _, ok := passed[name]; !ok {
			t.Errorf("%s is read by the %s service's code and documented in deploy/.env.example, but "+
				"deploy/docker-compose.yml never passes it to that service — an operator "+
				"setting it gets no effect and no error", name, service)
		}
	}
}

func repoFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// sourceVariables are the environment variables a source file of this package
// names, read from the file so the list cannot drift.
func sourceVariables(t *testing.T, file string) []string {
	t.Helper()
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var names []string
	for _, match := range envNamePattern.FindAllStringSubmatch(string(source), -1) {
		names = append(names, match[1])
	}
	if len(names) < 10 {
		t.Fatalf("found only %d variables in %s (%v); the pattern has stopped matching", len(names), file, names)
	}
	return names
}

// In config.go and runner.go a quoted SCREAMING_SNAKE_CASE literal is always a
// variable name.
var envNamePattern = regexp.MustCompile(`"([A-Z][A-Z0-9_]{2,})"`)

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

// serviceEnvironment are the keys, with their values, that the compose file's
// service passes in, parsed by indentation to avoid a YAML dependency. It
// understands the mapping form (`NAME: value`) only; a line whose key is not a
// plain variable name, which is what the list form (`- NAME=value`) parses to,
// fails the test naming that form. Fewer than minKeys keys means the parse has
// stopped matching, so a caller passes what its service really has.
func serviceEnvironment(t *testing.T, service string, minKeys int) map[string]string {
	t.Helper()
	env := map[string]string{}
	var inService, inEnv bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "  "+service+":"):
			inService = true
		case inService && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "":
			inService, inEnv = false, false // the next service begins
		case inService && strings.HasPrefix(line, "    environment:"):
			inEnv = true
		case inService && inEnv && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") &&
			strings.TrimSpace(line) != "":
			inEnv = false // the next key of this service
		case inService && inEnv && strings.HasPrefix(line, "      "):
			text := strings.TrimSpace(line)
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			name, value, ok := strings.Cut(text, ":")
			if !ok || !environmentName.MatchString(name) {
				t.Fatalf("the %s service's environment has the line %q, which is not NAME: value: "+
					"the list form (- NAME=value) is not understood here, write it as a mapping", service, text)
			}
			env[name] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	if len(env) < minKeys {
		t.Fatalf("the %s service parsed as %d environment keys, want at least %d; docker-compose.yml's shape has changed",
			service, len(env), minKeys)
	}
	return env
}

// environmentName is what an environment key of the mapping form looks like.
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// serviceLabels are the labels the compose file's service carries, parsed by
// indentation for the reason serviceEnvironment gives. A service with no
// labels block returns an empty map, which the test below reads as a failure
// rather than as "nothing to check".
func serviceLabels(t *testing.T, service string) map[string]string {
	t.Helper()
	labels := map[string]string{}
	var inService, inLabels bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "  "+service+":"):
			inService = true
		case inService && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "":
			inService, inLabels = false, false // the next service begins
		case inService && strings.HasPrefix(line, "    labels:"):
			inLabels = true
		case inService && inLabels && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") &&
			strings.TrimSpace(line) != "":
			inLabels = false // the next key of this service
		case inService && inLabels && strings.HasPrefix(line, "      "):
			name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
			if ok && !strings.HasPrefix(name, "#") {
				labels[name] = strings.Trim(strings.TrimSpace(value), `"'`)
			}
		}
	}
	return labels
}

// composePort is the port of a service's listener variable as the compose file
// sets it on that service — `QUERY_RUNNER_METRICS_ADDR: ":9101"` gives "9101".
// Only the named service's block is read, so the same variable on another
// service can neither satisfy the check nor stand in for a missing one.
func composePort(t *testing.T, service, variable string) string {
	t.Helper()
	var inService bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "  "+service+":"):
			inService = true
		case inService && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "":
			inService = false // the next service begins
		case inService && strings.HasPrefix(strings.TrimSpace(line), variable+":"):
			_, value, _ := strings.Cut(strings.TrimSpace(line), ":")
			address := strings.Trim(strings.TrimSpace(value), `"`)
			if _, port, ok := strings.Cut(address, ":"); ok {
				return port
			}
			t.Fatalf("%s in the %s service reads %q, which carries no port", variable, service, address)
		}
	}
	t.Fatalf("%s is not set on the %s service", variable, service)
	return ""
}

// scrapeTargets maps each job deploy/observability/prometheus.yml scrapes to
// its target. Jobs are compared whole: a job named core-api-dev is not
// core-api.
func scrapeTargets(t *testing.T) map[string]string {
	t.Helper()
	targets := map[string]string{}
	var job string
	for _, line := range strings.Split(repoFile(t, "deploy/observability/prometheus.yml"), "\n") {
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		if value, ok := strings.CutPrefix(trimmed, "job_name:"); ok {
			job = strings.Trim(strings.TrimSpace(value), `"'`)
			targets[job] = ""
		} else if value, ok := strings.CutPrefix(trimmed, "targets:"); ok && job != "" && targets[job] == "" {
			targets[job] = strings.Trim(strings.TrimSpace(value), `[]"' `)
		}
	}
	return targets
}

// buildTarget is the Dockerfile stage docker-compose.build.yml builds a
// service from.
func buildTarget(t *testing.T, service string) string {
	t.Helper()
	var inService bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.build.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "  "+service+":"):
			inService = true
		case inService && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") &&
			strings.TrimSpace(line) != "":
			inService = false // the next service begins
		case inService:
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "target:"); ok {
				return strings.TrimSpace(value)
			}
		}
	}
	t.Fatalf("the %s service sets no build target in docker-compose.build.yml", service)
	return ""
}

// exposedPorts are the ports one stage of backend/Dockerfile declares.
func exposedPorts(t *testing.T, stage string) map[string]bool {
	t.Helper()
	ports := map[string]bool{}
	var inStage bool
	for _, line := range strings.Split(repoFile(t, "backend/Dockerfile"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "FROM "):
			inStage = strings.HasSuffix(trimmed, " AS "+stage)
		case inStage && strings.HasPrefix(trimmed, "EXPOSE "):
			for _, port := range strings.Fields(strings.TrimPrefix(trimmed, "EXPOSE ")) {
				ports[port] = true
			}
		}
	}
	return ports
}

// TestAMetricsLabelNamesThePortItsListenerBinds is the drift guard. A service
// labelled for scraping is in one of two consistent states. With scrape "true"
// a collector reads the label and scrapes that port; the process listens on
// the address the variable sets; the image has to declare the port or
// Docker-API discovery produces no target for it at all; and the bundled
// Prometheus scrapes it by the static target in prometheus.yml: four places,
// one number, and nothing but this test keeps them equal. With scrape "false"
// the compose file sets the variable that disables the endpoint, so the label
// is not a promise the service breaks. That the endpoint exists in the first
// state is held by TestAScrapeLabelIsOnlyOnAServiceWhoseEndpointExists.
func TestAMetricsLabelNamesThePortItsListenerBinds(t *testing.T) {
	var scraped int
	for _, service := range []struct {
		name, variable, job, target string
	}{
		{name: "api", variable: "INTERNAL_ADDR", job: "core-api", target: "api:9090"},
		{name: "queryrunner", variable: "QUERY_RUNNER_METRICS_ADDR", job: "queryrunner", target: "queryrunner:9101"},
	} {
		labels := serviceLabels(t, service.name)
		if len(labels) == 0 {
			t.Fatalf("the %s service carries no labels; the metrics endpoint no longer describes itself", service.name)
		}
		switch labels["prometheus.io/scrape"] {
		case "true":
			scraped++
			t.Run(service.name, func(t *testing.T) {
				assertScrapedLabelsMatchListener(t, service.name, service.variable, service.job, service.target, labels)
			})
		case "false":
			t.Run(service.name+" is not scraped", func(t *testing.T) {
				rule := endpointRules[service.name]
				value, set := serviceEnvironment(t, service.name, 0)[rule.variable]
				if !set {
					t.Fatalf("%s is labelled prometheus.io/scrape: \"false\" but its environment never sets %s, "+
						"so whether the endpoint exists is left to the binary's default", service.name, rule.variable)
				}
				if rule.serves(composeDefault(value)) {
					t.Errorf("%s is labelled prometheus.io/scrape: \"false\" but %s is %q and the endpoint exists: "+
						"set the label to \"true\", or disable the endpoint", service.name, rule.variable, value)
				}
			})
		default:
			t.Errorf("%s carries prometheus.io/scrape %q, want exactly \"true\" or \"false\"",
				service.name, labels["prometheus.io/scrape"])
		}
	}
	// With every label "false" nothing below would run, and the port could
	// drift unseen.
	if scraped == 0 {
		t.Fatal("no service in docker-compose.yml is scraped; the port-drift guard has nothing to guard")
	}
}

func assertScrapedLabelsMatchListener(t *testing.T, service, variable, job, target string, labels map[string]string) {
	t.Helper()
	if labels["prometheus.io/path"] != "/metrics" {
		t.Errorf("prometheus.io/path is %q, want \"/metrics\"", labels["prometheus.io/path"])
	}
	// The job name is what a dashboard written against the bundled
	// Prometheus selects on, so it has to be the one that file scrapes.
	if labels["prometheus.io/job"] != job {
		t.Errorf("prometheus.io/job is %q, want %q", labels["prometheus.io/job"], job)
	}
	targets := scrapeTargets(t)
	scraped, ok := targets[job]
	if !ok {
		t.Errorf("prometheus.yml scrapes no job named %q; it scrapes %v", job, targets)
	}

	port := composePort(t, service, variable)
	if labels["prometheus.io/port"] != port {
		t.Errorf("prometheus.io/port is %q but %s binds %q: a collector would scrape the wrong port",
			labels["prometheus.io/port"], variable, port)
	}
	if scraped != target || !strings.HasSuffix(scraped, ":"+port) {
		t.Errorf("prometheus.yml scrapes job %q at %q but %s binds %q: the bundled Prometheus would scrape a closed port",
			job, scraped, variable, port)
	}
	stage := buildTarget(t, service)
	if exposed := exposedPorts(t, stage); !exposed[port] {
		t.Errorf("the %s stage does not EXPOSE %s, so Docker-API discovery produces no target for it; it declares %v",
			stage, port, exposed)
	}
}

// composeServices are the names under the compose file's top-level `services:`
// key.
func composeServices(t *testing.T) []string {
	t.Helper()
	var names []string
	var inServices bool
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		switch {
		case strings.HasPrefix(line, "services:"):
			inServices = true
		case inServices && line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#"):
			inServices = false // volumes:, networks:
		case inServices && strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   "):
			if name, rest, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && rest == "" && !strings.HasPrefix(name, "#") {
				names = append(names, name)
			}
		}
	}
	if len(names) < 10 {
		t.Fatalf("docker-compose.yml parsed as %d services (%v); its shape has changed", len(names), names)
	}
	return names
}

// composeDefault is the value an environment assignment falls back to:
// `${NAME:-fallback}` gives "fallback" and a plain value gives itself.
func composeDefault(value string) string {
	inner, ok := strings.CutPrefix(value, "${")
	if !ok {
		return value
	}
	inner = strings.TrimSuffix(inner, "}")
	_, fallback, _ := strings.Cut(inner, ":-")
	return fallback
}

// endpointRules say, per service, which of its own environment variables makes
// the endpoint its prometheus.io labels advertise exist, and what that
// variable's value must not defeat. A service labelled for scraping needs a
// row here, or TestAScrapeLabelIsOnlyOnAServiceWhoseEndpointExists fails.
var endpointRules = map[string]struct {
	variable string
	serves   func(value string) bool
	want     string
}{
	"api": {
		variable: "METRICS_BACKEND",
		serves:   func(value string) bool { return value == "prometheus" },
		want:     "default to prometheus: with log or none the path answers 404",
	},
	"queryrunner": {
		variable: "QUERY_RUNNER_METRICS_ADDR",
		serves:   func(value string) bool { return value != MetricsOff },
		want:     "not be " + MetricsOff + ", which turns the listener off",
	},
}

// A scrape label is a promise that the endpoint exists. The compose file sets
// the variable that decides it on the same service, so the promise can be read
// from there: this test finds every service labelled for scraping and checks
// it against the rule its service needs.
func TestAScrapeLabelIsOnlyOnAServiceWhoseEndpointExists(t *testing.T) {
	var scraped, withLabel int
	for _, service := range composeServices(t) {
		value, ok := serviceLabels(t, service)["prometheus.io/scrape"]
		if ok {
			withLabel++
		}
		if ok && value != "true" && value != "false" {
			t.Errorf("%s carries prometheus.io/scrape with a value this test could not read, %q: "+
				"write it as exactly \"true\" or \"false\", with no trailing comment", service, value)
			continue
		}
		if value != "true" {
			continue
		}
		scraped++
		t.Run(service, func(t *testing.T) {
			rule, ok := endpointRules[service]
			if !ok {
				t.Fatalf("%s is labelled prometheus.io/scrape: \"true\" but this test knows no rule for it: "+
					"a new scraped service needs a row in endpointRules naming the variable that makes its endpoint exist", service)
			}
			value, passed := serviceEnvironment(t, service, 0)[rule.variable]
			if !passed {
				t.Fatalf("%s is labelled prometheus.io/scrape: \"true\" but its environment never sets %s, "+
					"so the endpoint it advertises is left to the binary's default", service, rule.variable)
			}
			if !rule.serves(composeDefault(value)) {
				t.Errorf("%s is labelled prometheus.io/scrape: \"true\" but %s is %q; it must %s",
					service, rule.variable, value, rule.want)
			}
		})
	}
	if scraped == 0 {
		t.Fatal("no service in docker-compose.yml is labelled prometheus.io/scrape: \"true\"; the parse has stopped matching")
	}
	// serviceLabels reads only the mapping form (`key: value`). A label written
	// as a list item (`- prometheus.io/scrape=true`) is valid compose it would
	// not see, and the service would then escape every rule above.
	if mentions := scrapeLabelMentions(t); mentions != withLabel {
		t.Fatalf("docker-compose.yml mentions prometheus.io/scrape on %d lines but %d services were read as carrying it: "+
			"a label in list form (- prometheus.io/scrape=true) is not understood here, write it as a mapping (prometheus.io/scrape: \"true\")",
			mentions, withLabel)
	}
}

// scrapeLabelMentions counts the compose file's non-comment lines naming the
// scrape label, in whatever form.
func scrapeLabelMentions(t *testing.T) int {
	t.Helper()
	var count int
	for _, line := range strings.Split(repoFile(t, "deploy/docker-compose.yml"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, "prometheus.io/scrape") {
			count++
		}
	}
	return count
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

func TestTheParticipantsBudgetsDefaultToTheOlympiadsFigures(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.QueryPerMinute != 60 {
		t.Errorf("QueryPerMinute = %d, want 60", cfg.QueryPerMinute)
	}
	if cfg.PlayReadPerMinute != 120 {
		t.Errorf("PlayReadPerMinute = %d, want 120", cfg.PlayReadPerMinute)
	}
	if cfg.AnswerRatePerMinute != 12 {
		t.Errorf("AnswerRatePerMinute = %d, want 12", cfg.AnswerRatePerMinute)
	}
}

func TestTheReadBudgetIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("PLAY_READ_PER_MINUTE", "300")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.PlayReadPerMinute != 300 {
		t.Errorf("PlayReadPerMinute = %d, want 300", cfg.PlayReadPerMinute)
	}

	// Under thirty a minute the play screen cannot finish loading twice.
	for _, value := range []string{"0", "29", "601", "-1", "many"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
			t.Setenv("PLAY_READ_PER_MINUTE", value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted PLAY_READ_PER_MINUTE=%s, want error", value)
			} else if !strings.Contains(err.Error(), "PLAY_READ_PER_MINUTE") {
				t.Errorf("error %q does not name PLAY_READ_PER_MINUTE", err)
			}
		})
	}
}

func TestTheAnswerRateIsConfigurableAndBounded(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ANSWER_RATE_PER_MINUTE", "20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.AnswerRatePerMinute != 20 {
		t.Errorf("AnswerRatePerMinute = %d, want 20", cfg.AnswerRatePerMinute)
	}

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

func TestExportConcurrencyIsCheckedAgainstThePoolItIsAShareOf(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("CORE_DB_POOL_MAX", "5")

	t.Setenv("EXPORT_CONCURRENCY", "10")
	if _, err := Load(); err == nil {
		t.Error("Load() accepted EXPORT_CONCURRENCY=10 against CORE_DB_POOL_MAX=5, want error")
	}

	t.Setenv("EXPORT_CONCURRENCY", "2")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ExportConcurrency != 2 {
		t.Errorf("ExportConcurrency = %d, want 2", cfg.ExportConcurrency)
	}

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

	for _, raw := range []string{"-1s", "31s", "5m", "soon"} {
		t.Setenv("SESSION_ACCOUNT_CACHE_TTL", raw)
		if _, err := Load(); err == nil {
			t.Errorf("Load() accepted SESSION_ACCOUNT_CACHE_TTL=%s, want error", raw)
		}
	}
}

func TestTheDeviceCookieSecretIsRequiredOutsideDevelopment(t *testing.T) {
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

func TestTheQueryRunnerTokenIsNotRequiredWithoutAConsole(t *testing.T) {
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("ENV", "production")
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
	t.Setenv("QUERY_RUNNER_ADDR", "")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() without a console refused a missing token: %v", err)
	}
}

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

// An operator who copies .env.example must trust only the two pinned proxy
// containers, not every container on the network (CLAUDE.md rule 9).
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
		"SMTP_PASSWORD":        "change-me-to-the-relay-password",
	} {
		t.Run(name, func(t *testing.T) {
			production(t)
			if name == "GAME_AUTHOR_PASSWORD" || name == "GAME_PROVISIONER_DSN" {
				t.Setenv("GAME_PROVISIONER_DSN", "postgres://game:real@pg-game:5432/game")
				t.Setenv("GAME_AUTHOR_PASSWORD", "a-real-author-password")
			}
			if name == "SMTP_PASSWORD" {
				productionMail(t)
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

// productionMail is a production configuration with mail on, every SMTP
// variable set so none is inherited from the shell.
func productionMail(t *testing.T) {
	t.Helper()
	t.Setenv("ENV", "production")
	t.Setenv("CORE_DB_DSN", "postgres://user:pass@localhost:5432/core")
	t.Setenv("DEVICE_COOKIE_SECRET", strings.Repeat("s", 32))
	t.Setenv("SMTP_HOST", "smtp.usm.md")
	t.Setenv("SMTP_PORT", "")
	t.Setenv("SMTP_USERNAME", "olympiad@usm.md")
	t.Setenv("SMTP_PASSWORD", "a-real-relay-password")
	t.Setenv("SMTP_FROM", "Olimpiada USM <olympiad@usm.md>")
	t.Setenv("SMTP_TLS", "")
	t.Setenv("SMTP_MAX_PER_MINUTE", "")
	t.Setenv("PUBLIC_URL", "https://olympdb.usm.md")
}

func TestMailIsOffWithoutAnSMTPHost(t *testing.T) {
	productionMail(t)
	for _, name := range []string{"SMTP_HOST", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM", "PUBLIC_URL"} {
		t.Setenv(name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() without any SMTP variable = %v, want mail off", err)
	}
	if cfg.MailEnabled() {
		t.Error("MailEnabled() = true without SMTP_HOST")
	}
}

func TestMailSettingsAreReadWithTheirDefaults(t *testing.T) {
	productionMail(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if !cfg.MailEnabled() {
		t.Error("MailEnabled() = false with SMTP_HOST set")
	}
	if cfg.SMTPHost != "smtp.usm.md" || cfg.SMTPUsername != "olympiad@usm.md" ||
		cfg.SMTPPassword != "a-real-relay-password" || cfg.SMTPFrom != "Olimpiada USM <olympiad@usm.md>" {
		t.Errorf("SMTP settings = %q %q %q, not the configured values", cfg.SMTPHost, cfg.SMTPUsername, cfg.SMTPFrom)
	}
	if cfg.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want the default 587", cfg.SMTPPort)
	}
	if cfg.SMTPTLS != mail.TLSStartTLS {
		t.Errorf("SMTPTLS = %q, want the default starttls", cfg.SMTPTLS)
	}
	if cfg.PublicURL != "https://olympdb.usm.md" {
		t.Errorf("PublicURL = %q", cfg.PublicURL)
	}
	if cfg.SMTPMaxPerMinute != 25 {
		t.Errorf("SMTPMaxPerMinute = %d, want the default 25, under Microsoft 365's 30 a minute", cfg.SMTPMaxPerMinute)
	}

	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_TLS", "tls")
	t.Setenv("SMTP_MAX_PER_MINUTE", "100")
	t.Setenv("PUBLIC_URL", "https://olympdb.usm.md/")
	if cfg, err = Load(); err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if cfg.SMTPPort != 465 || cfg.SMTPTLS != mail.TLSImplicit {
		t.Errorf("SMTPPort, SMTPTLS = %d, %q, want 465, tls", cfg.SMTPPort, cfg.SMTPTLS)
	}
	if cfg.SMTPMaxPerMinute != 100 {
		t.Errorf("SMTPMaxPerMinute = %d, want 100", cfg.SMTPMaxPerMinute)
	}
	if cfg.PublicURL != "https://olympdb.usm.md" {
		t.Errorf("PublicURL = %q, want it without the trailing slash", cfg.PublicURL)
	}
}

func TestARelayWithoutCredentialsIsAccepted(t *testing.T) {
	productionMail(t)
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")

	if _, err := Load(); err != nil {
		t.Errorf("Load() for a relay that takes no AUTH = %v", err)
	}
}

func TestMailWithAMissingOrInvalidCompanionIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, value, named string }{
		{"SMTP_FROM", "", "SMTP_FROM"},
		{"SMTP_FROM", "olympiad", "SMTP_FROM"},
		{"SMTP_FROM", "a@usm.md, b@usm.md", "SMTP_FROM"},
		{"SMTP_FROM", "=?utf-8?q?USM=0D=0ABcc=3A_x=40y=2Emd?= <olympiad@usm.md>", "SMTP_FROM"},
		{"PUBLIC_URL", "", "PUBLIC_URL"},
		{"PUBLIC_URL", "olympdb.usm.md", "PUBLIC_URL"},
		{"PUBLIC_URL", "http://olympdb.usm.md", "PUBLIC_URL"},
		{"PUBLIC_URL", "https://olympdb.usm.md/?next=1", "PUBLIC_URL"},
		{"PUBLIC_URL", "https://olympdb.usm.md/#top", "PUBLIC_URL"},
		{"PUBLIC_URL", "https://admin@olympdb.usm.md", "PUBLIC_URL"},
		{"SMTP_PASSWORD", "", "SMTP_PASSWORD"},
		{"SMTP_USERNAME", "", "SMTP_USERNAME"},
		{"SMTP_PORT", "smtp", "SMTP_PORT"},
		{"SMTP_PORT", "0", "SMTP_PORT"},
		{"SMTP_PORT", "65536", "SMTP_PORT"},
		{"SMTP_TLS", "ssl", "SMTP_TLS"},
		{"SMTP_HOST", "smtp.usm.md:587", "SMTP_HOST"},
		{"SMTP_HOST", "smtp://smtp.usm.md", "SMTP_HOST"},
		{"SMTP_MAX_PER_MINUTE", "many", "SMTP_MAX_PER_MINUTE"},
		{"SMTP_MAX_PER_MINUTE", "0", "SMTP_MAX_PER_MINUTE"},
		{"SMTP_MAX_PER_MINUTE", "601", "SMTP_MAX_PER_MINUTE"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			productionMail(t)
			t.Setenv(tc.name, tc.value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.named) {
				t.Fatalf("Load() = %v, want an error naming %s", err, tc.named)
			}
			if strings.Contains(err.Error(), "a-real-relay-password") {
				t.Errorf("the refusal repeats the password: %q", err)
			}
		})
	}
}

func TestAPlainRelayIsRefusedOutsideDevelopment(t *testing.T) {
	productionMail(t)
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("SMTP_TLS", "none")

	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_TLS") {
		t.Errorf("Load() with SMTP_TLS=none in production = %v, want an error naming SMTP_TLS", err)
	}
}

func TestADevelopmentRelayMayBePlainAndLocal(t *testing.T) {
	productionMail(t)
	t.Setenv("ENV", "development")
	t.Setenv("SMTP_HOST", "localhost")
	t.Setenv("SMTP_PORT", "1025")
	t.Setenv("SMTP_TLS", "none")
	t.Setenv("SMTP_USERNAME", "")
	t.Setenv("SMTP_PASSWORD", "")
	t.Setenv("PUBLIC_URL", "http://localhost:3000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() for a development relay = %v", err)
	}
	if cfg.SMTPTLS != mail.TLSNone || cfg.PublicURL != "http://localhost:3000" {
		t.Errorf("SMTPTLS, PublicURL = %q, %q", cfg.SMTPTLS, cfg.PublicURL)
	}

	// Credentials still never cross a plain connection.
	t.Setenv("SMTP_USERNAME", "dev")
	t.Setenv("SMTP_PASSWORD", "dev")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "SMTP_TLS") {
		t.Errorf("Load() with credentials and SMTP_TLS=none = %v, want an error naming SMTP_TLS", err)
	}
}
