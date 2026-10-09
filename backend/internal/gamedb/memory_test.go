package gamedb_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
)

// testProcessCapBytes is the test cluster's cap: GAME_DB_PROCESS_MEMORY_BYTES,
// from which the dev overlay sets ulimits.data, or 256 MiB when unset.
func testProcessCapBytes(t *testing.T) int64 {
	t.Helper()
	raw := os.Getenv("GAME_DB_PROCESS_MEMORY_BYTES")
	if raw == "" {
		return 256 << 20
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("GAME_DB_PROCESS_MEMORY_BYTES = %q is not a number", raw)
	}
	return v
}

// Proved against the deployment's own configuration (CLAUDE.md rule 10).
func TestVerifyProcessMemoryCapPassesUnderTheRealCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, testProcessCapBytes(t)); err != nil {
		t.Fatalf("the self-check failed on a capped cluster: %v", err)
	}
}

// Both claimed caps are in the supported range, so the error is the mismatch.
func TestVerifyProcessMemoryCapCatchesAMismatchedLimit(t *testing.T) {
	pool := admin(t)
	real := testProcessCapBytes(t)

	for _, claimed := range []int64{real + 256<<20, real - 64<<20} {
		if claimed < gamedb.MinVerifiableCapBytes || claimed > gamedb.MaxVerifiableCapBytes {
			continue
		}
		err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, claimed)
		if err == nil {
			t.Fatalf("the self-check passed while claiming a %d-byte cap against the real %d", claimed, real)
		}
		if !strings.Contains(err.Error(), "not the configured") {
			t.Fatalf("claimed %d: error did not name the limit mismatch: %v", claimed, err)
		}
	}
}

func TestVerifyProcessMemoryCapRefusesAnUnsupportedCap(t *testing.T) {
	pool := admin(t)

	for _, claimed := range []int64{
		gamedb.MinVerifiableCapBytes - 1,
		16 << 20,
		gamedb.MaxVerifiableCapBytes + 1,
		8 << 30,
	} {
		err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, claimed)
		if err == nil || !strings.Contains(err.Error(), "unsupported cap") {
			t.Fatalf("claimed %d: error = %v, want an unsupported-cap refusal", claimed, err)
		}
	}
}

// testContainerMemoryBytes is the test container's limit: GAME_DB_MEMORY_BYTES,
// with the deployment's default.
func testContainerMemoryBytes(t *testing.T) int64 {
	t.Helper()
	raw := os.Getenv("GAME_DB_MEMORY_BYTES")
	if raw == "" {
		return config.DefaultGameDBMemoryBytes
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("GAME_DB_MEMORY_BYTES = %q is not a number", raw)
	}
	return v
}

func TestVerifyContainerMemoryLimitMatchesTheRealLimit(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyContainerMemoryLimit(t.Context(), pool, gamedb.CgroupMemoryLimitFile, testContainerMemoryBytes(t)); err != nil {
		t.Fatalf("the container limit check failed on a correctly sized cluster: %v", err)
	}
}

func TestVerifyContainerMemoryLimitCatchesAMismatch(t *testing.T) {
	pool := admin(t)

	err := gamedb.VerifyContainerMemoryLimit(t.Context(), pool, gamedb.CgroupMemoryLimitFile, testContainerMemoryBytes(t)+(1<<30))
	if err == nil {
		t.Fatal("a limit one gibibyte off the real one was accepted")
	}
	if !strings.Contains(err.Error(), "not the configured") {
		t.Fatalf("error did not name the mismatch: %v", err)
	}
	if errors.Is(err, gamedb.ErrContainerLimitUnreadable) {
		t.Fatalf("a readable, wrong limit was reported as unreadable: %v", err)
	}
}

func TestVerifyContainerMemoryLimitReportsAnUnreadableLimit(t *testing.T) {
	pool := admin(t)

	err := gamedb.VerifyContainerMemoryLimit(t.Context(), pool, "/sys/fs/cgroup/no-such-file", testContainerMemoryBytes(t))
	if !errors.Is(err, gamedb.ErrContainerLimitUnreadable) {
		t.Fatalf("error = %v, want ErrContainerLimitUnreadable", err)
	}
}

func TestVerifyMemorySettingsMatchesTheCluster(t *testing.T) {
	pool := admin(t)

	want := gamedb.MemorySettings{
		AuthorConnectionLimit: config.MaxBuildSessions,
		MaxParallelWorkers:    config.MaxParallelWorkers,
		AutovacuumWorkers:     config.AutovacuumWorkers,
	}
	if err := gamedb.VerifyMemorySettings(t.Context(), pool, want); err != nil {
		t.Fatalf("the cluster does not match the memory arithmetic: %v", err)
	}
}

func TestVerifyMemorySettingsNamesEveryDrift(t *testing.T) {
	pool := admin(t)

	err := gamedb.VerifyMemorySettings(t.Context(), pool, gamedb.MemorySettings{
		AuthorConnectionLimit: config.MaxBuildSessions + 1,
		MaxParallelWorkers:    config.MaxParallelWorkers + 1,
		AutovacuumWorkers:     config.AutovacuumWorkers + 1,
	})
	if err == nil {
		t.Fatal("three drifted settings were accepted")
	}
	for _, name := range []string{"CONNECTION LIMIT", "max_parallel_workers", "autovacuum_max_workers"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}
