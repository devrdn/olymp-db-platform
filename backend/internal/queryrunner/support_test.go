package queryrunner_test

import (
	"strconv"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

// These tests run against a real game cluster, as the participant's own role.
// Nothing here is faked: the whole component is about what happens between a
// deadline, a semaphore and a database, and none of those has a useful
// stand-in. Without GAME_DB_DSN they skip — see gamedbtest.

func setup(t *testing.T) (*queryrunner.Runner, string) {
	t.Helper()
	return setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())
}

func setupWith(t *testing.T, limits queryrunner.Limits, checker *checker.Checker) (*queryrunner.Runner, string) {
	t.Helper()

	database := gamedbtest.Scratch(t)
	gamedbtest.Run(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY, note text)`,
		`INSERT INTO evidence VALUES (1, 'a knife'), (2, 'a letter')`,
		`GRANT USAGE ON SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+gamedb.RoleReader+`, `+gamedb.RoleWriter,
		// The writer may keep its own objects, as a read-write template grants
		// (internal/gamedb), and nothing else: which game tables it may write
		// is granted by the tests that are about writing.
		`CREATE SCHEMA work`,
		`GRANT USAGE, CREATE ON SCHEMA work TO `+gamedb.RoleWriter,
	)

	return queryrunner.New(clusterFor(t, database), checker, limits), database
}

// clusterFor connects as both participant roles, so a test may run either
// kind of contest against the same database.
func clusterFor(t *testing.T, database string) *queryrunner.Cluster {
	t.Helper()

	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword, database),
		gamedbtest.DSN(t, gamedb.RoleWriter, gamedbtest.WriterPassword, database))
	if err != nil {
		t.Fatalf("building the cluster connector: %v", err)
	}
	return cluster
}

// oneParticipant is fixed because most tests are about the query rather than
// about who asked; the tests that are about who use `other`.
var oneParticipant = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func request(database, sql string) queryrunner.Request {
	return queryrunner.Request{
		Registration: oneParticipant,
		Database:     database,
		SQL:          sql,
		Policy:       sqlpolicy.ReadOnly(),
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
