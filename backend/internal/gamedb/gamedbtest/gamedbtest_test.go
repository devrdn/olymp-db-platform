package gamedbtest

import (
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

// The test cluster's address with the database swapped for "postgres", as a
// wrong GAME_DB_DSN would look.
func TestConnectRefusesAClusterThatIsNotATestCluster(t *testing.T) {
	configured := os.Getenv("GAME_DB_DSN")
	if configured == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game`")
	}
	parsed, err := url.Parse(configured)
	if err != nil {
		t.Fatalf("GAME_DB_DSN is not a URL: %v", err)
	}
	parsed.Path = "/postgres"
	t.Setenv("GAME_DB_DSN", parsed.String())

	// Reset the package state connect fills, before and after.
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		once, pool, dsn, open = sync.Once{}, nil, "", nil
	})
	once, pool, dsn, open = sync.Once{}, nil, "", nil

	once.Do(connect)
	if !errors.Is(open, storagetest.ErrNotATestDatabase) {
		t.Fatalf("connect() to the maintenance database left open = %v, want ErrNotATestDatabase", open)
	}
	if pool != nil {
		t.Fatal("connect() kept a pool on a cluster it refused")
	}
}
