package main

import (
	"os"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

// testStores connects to the test clusters in CORE_DB_DSN and GAME_DB_DSN,
// as openStores connects to an installation's, and skips without either.
func testStores(t *testing.T) *stores {
	t.Helper()

	if os.Getenv(storagetest.CoreDSNVar) == "" || !gamedbtest.Configured() {
		t.Skip("set CORE_DB_DSN and GAME_DB_DSN to test cluster databases to run the database tests")
	}
	core, err := storagetest.OpenCore(t.Context(), nil)
	if err != nil {
		t.Fatalf("open the core test database: %v", err)
	}
	t.Cleanup(core.Close)

	// The game pool is gamedbtest's, shared by the whole binary, so it is
	// not closed here.
	game := gamedbtest.Admin(t)
	user, password := gamedbtest.AdminCredentials(t)
	provisioner, err := gamedb.NewProvisioner(game, gamedbtest.DSN(t, user, password, "postgres"), "")
	if err != nil {
		t.Fatalf("open the provisioner: %v", err)
	}
	return &stores{core: core, game: game, provisioner: provisioner}
}
