package gamedb_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
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
