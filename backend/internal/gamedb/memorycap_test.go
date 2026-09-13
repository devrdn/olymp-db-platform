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
// force: a quarter of the cap allocates, the cap plus a margin is refused with
// out_of_memory. This is the guarantee the whole memory story rests on, proved
// against the deployment's own configuration (CLAUDE.md rule 10).
func TestVerifyProcessMemoryCapPassesUnderTheRealCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, testProcessCapBytes); err != nil {
		t.Fatalf("the self-check failed on a capped cluster: %v", err)
	}
}

// It fails, and says why, when told the cap is far larger than it is: the
// "over the cap" probe then asks for more than a gibibyte, which the real
// 256 MiB cap refuses — but so would the check's own expectation, so this
// case instead proves the other direction, that a cap claimed much larger than
// reality is caught. A cap of eight gibibytes means the "over" probe asks for
// ~8 GiB (refused by the real cap, good) but the "quarter" probe asks for
// 2 GiB, which the real 256 MiB cap refuses — so the check reports that
// legitimate-sized allocation failing, which is exactly the misconfiguration
// signal.
func TestVerifyProcessMemoryCapCatchesAClaimTooLarge(t *testing.T) {
	pool := admin(t)

	err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, 8<<30)
	if err == nil {
		t.Fatal("the self-check passed while claiming a cap eight times the real one")
	}
	if !strings.Contains(err.Error(), "under the") {
		t.Fatalf("error did not point at the too-large claim: %v", err)
	}
}

// A non-positive cap is a misconfiguration, not something to probe.
func TestVerifyProcessMemoryCapRejectsANonPositiveCap(t *testing.T) {
	pool := admin(t)

	if err := gamedb.VerifyProcessMemoryCap(t.Context(), pool, 0); err == nil {
		t.Fatal("a zero cap was accepted")
	}
}
