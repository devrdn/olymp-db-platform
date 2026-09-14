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

// testProcessCapBytes is the cap the test cluster runs with. It comes from
// GAME_DB_PROCESS_MEMORY_BYTES — the variable the dev overlay interpolates
// pg-game-test's ulimits.data from — so the tests follow whatever cap the
// cluster was created with, and 256 MiB (the deploy default) when unset.
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

// The deploy-time self-check passes on a cluster whose per-process cap is in
// force and matches the configured value: the reported "Max data size" equals
// the cap, a quarter of the cap allocates, and more than the cap is refused
// with out_of_memory. This is the guarantee the whole memory story rests on,
// proved against the deployment's own configuration (CLAUDE.md rule 10).
func TestVerifyProcessMemoryCapPassesUnderTheRealCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, testProcessCapBytes(t)); err != nil {
		t.Fatalf("the self-check failed on a capped cluster: %v", err)
	}
}

// It fails, and says so, when told a cap that differs from the one the kernel
// is enforcing — the misconfiguration the /proc check exists to catch (a
// ulimit that drifted from GAME_DB_PROCESS_MEMORY_BYTES). Both claims are inside
// the supported range, so the error is the mismatch, not the range.
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

// A cap outside the range the check can prove is refused as unsupported before
// anything is allocated: below it the quarter-cap probe says nothing, above it
// the probes cannot be built from values PostgreSQL will hold.
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

// testContainerMemoryBytes is the memory limit the test cluster's container
// runs with: GAME_DB_MEMORY_BYTES, the variable the dev overlay interpolates
// pg-game-test's limit from, with the same default as the deployment.
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

// The container's real memory limit — read by the backend from its own cgroup
// — must be the one the Query Runner's arithmetic was checked against. Proved
// on pg-game-test, whose limit is interpolated from the same variable.
func TestVerifyContainerMemoryLimitMatchesTheRealLimit(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyContainerMemoryLimit(t.Context(), pool, gamedb.CgroupMemoryLimitFile, testContainerMemoryBytes(t)); err != nil {
		t.Fatalf("the container limit check failed on a correctly sized cluster: %v", err)
	}
}

// A limit that differs from the configured one — a container created from a
// different value, or with no limit at all — is refused and named.
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

// When the limit cannot be read at all (cgroup v1, no cgroup namespace) the
// check says so with its own sentinel, so the deploy can fail clearly — or,
// with the documented opt-out, carry on knowingly.
func TestVerifyContainerMemoryLimitReportsAnUnreadableLimit(t *testing.T) {
	pool := admin(t)

	err := gamedb.VerifyContainerMemoryLimit(t.Context(), pool, "/sys/fs/cgroup/no-such-file", testContainerMemoryBytes(t))
	if !errors.Is(err, gamedb.ErrContainerLimitUnreadable) {
		t.Fatalf("error = %v, want ErrContainerLimitUnreadable", err)
	}
}

// The Query Runner sizes the game cluster's memory from constants that restate
// settings owned elsewhere: how many build sessions the game_author role may
// hold, and how many parallel and autovacuum workers the cluster runs. The
// platform layer cannot import this package to share them, so the deploy (and
// this test, on the prepared test cluster) reads each back and compares.
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

// Every drifted setting is named, not only the first.
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
