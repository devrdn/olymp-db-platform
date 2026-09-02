package queryrunner_test

import (
	"strconv"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// These tests run against a real game cluster, as the participant's own role.
// Nothing here is faked: the whole component is about what happens between a
// deadline, a semaphore and a database, and none of those has a useful
// stand-in. Without GAME_DB_DSN they skip — see gamedbtest.

func setup(t *testing.T) (*queryrunner.Runner, string) {
	t.Helper()
	return setupWith(t, queryrunner.DefaultLimits(), sqlpolicy.NewChecker())
}

func setupWith(t *testing.T, limits queryrunner.Limits, checker *sqlpolicy.Checker) (*queryrunner.Runner, string) {
	t.Helper()

	database := gamedbtest.Scratch(t)
	gamedbtest.Run(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY, note text)`,
		`INSERT INTO evidence VALUES (1, 'a knife'), (2, 'a letter')`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader,
	)

	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword, database))
	if err != nil {
		t.Fatalf("building the cluster connector: %v", err)
	}

	return queryrunner.New(cluster, checker, limits), database
}

// request is one participant asking one question. The participant is fixed
// because most tests are about the query rather than about who asked.
func request(database, sql string) queryrunner.Request {
	return queryrunner.Request{
		Participant: "participant-under-test",
		Database:    database,
		SQL:         sql,
		Policy:      sqlpolicy.ReadOnly(),
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
