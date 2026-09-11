package gamedbtest

import (
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
)

// A cluster whose maintenance database is not a test database is refused at
// connect, before any helper could prepare a role or create a database on it.
//
// Proved against a real server the way a wrong GAME_DB_DSN would meet it: the
// test cluster's own address with the database swapped for "postgres", which
// every cluster has and which does not end in _test. Nothing is prepared on
// it — the refusal is the whole point.
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

	// connect fills package state that every other helper reads; this binary
	// has no other test using it, and it is put back regardless.
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
