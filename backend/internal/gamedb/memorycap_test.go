package gamedb_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
)

// The cap the deployment sets and the test cluster runs with (deploy
// docker-compose.dev.yml ulimits.data, matching pg-game's).
const testProcessCapBytes = 256 << 20

// The deploy-time self-check passes on a cluster whose per-process cap is in
// force and matches the configured value: the reported "Max data size" equals
// the cap, a quarter of the cap allocates, and the cap plus a margin is refused
// with out_of_memory. This is the guarantee the whole memory story rests on,
// proved against the deployment's own configuration (CLAUDE.md rule 10).
func TestVerifyProcessMemoryCapPassesUnderTheRealCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, testProcessCapBytes); err != nil {
		t.Fatalf("the self-check failed on a capped cluster: %v", err)
	}
}

// It fails, and says so, when told a cap that differs from the one the kernel
// is enforcing — the misconfiguration the /proc check exists to catch (a
// ulimit that drifted from GAME_DB_PROCESS_MEMORY_BYTES). Both directions are
// caught by the exact-limit comparison, so the error names the mismatch rather
// than an allocation outcome.
func TestVerifyProcessMemoryCapCatchesAMismatchedLimit(t *testing.T) {
	pool := admin(t)

	for _, claimed := range []int64{512 << 20, 300 << 20} {
		err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, claimed)
		if err == nil {
			t.Fatalf("the self-check passed while claiming a %d-byte cap against the real 256 MiB", claimed)
		}
		if !strings.Contains(err.Error(), "not the configured") {
			t.Fatalf("claimed %d: error did not name the limit mismatch: %v", claimed, err)
		}
	}
}

// A cap below the check's floor is a misconfiguration, not something to probe.
func TestVerifyProcessMemoryCapRejectsATooSmallCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, 16<<20); err == nil {
		t.Fatal("a cap below the supported floor was accepted")
	}
}
