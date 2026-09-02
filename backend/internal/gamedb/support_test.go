package gamedb_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The arrangement these tests need lives in gamedbtest, because the Query
// Runner's tests need the same one and two copies of it would drift. What
// stays here is only the naming this package's own tests read best with.
const (
	testReaderPassword = gamedbtest.ReaderPassword
	roleReader         = gamedb.RoleReader
)

func admin(t *testing.T) *pgxpool.Pool { t.Helper(); return gamedbtest.Admin(t) }

func requireCluster(t *testing.T) { t.Helper(); gamedbtest.Admin(t) }

func scratchDatabase(t *testing.T) string { t.Helper(); return gamedbtest.Scratch(t) }

func asOwner(t *testing.T, database string, statements ...string) {
	t.Helper()
	gamedbtest.Run(t, database, statements...)
}

func connectAs(t *testing.T, user, password, database string) *pgx.Conn {
	t.Helper()
	return gamedbtest.Connect(t, user, password, database)
}

// connectAsOwner opens a connection as the provisioning role.
func connectAsOwner(t *testing.T, database string) *pgx.Conn {
	t.Helper()
	user, password := gamedbtest.AdminCredentials(t)
	return gamedbtest.Connect(t, user, password, database)
}

// tryConnectAs reports the error instead of failing, for the tests whose
// subject is that a connection is refused.
func tryConnectAs(t *testing.T, user, password, database string) error {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), gamedbtest.DSN(t, user, password, database))
	if err == nil {
		_ = conn.Close(t.Context())
	}
	return err
}

// refused runs the statement and returns the error, failing if it was allowed.
func refused(t *testing.T, conn *pgx.Conn, sql string) error {
	t.Helper()

	_, err := conn.Exec(t.Context(), sql)
	if err == nil {
		t.Fatalf("the database allowed: %s", sql)
	}
	return err
}
