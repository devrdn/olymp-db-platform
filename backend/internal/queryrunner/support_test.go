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

func setupWith(t *testing.T, limits queryrunner.Limits, validator queryrunner.Validator) (*queryrunner.Runner, string) {
	t.Helper()

	database := seeded(t)
	return runnerFor(t, database, limits, validator), database
}

// seeded creates a scratch database holding the fixture every runner test
// reads, granted to both participant roles.
func seeded(t *testing.T) string {
	t.Helper()

	database := gamedbtest.Scratch(t)
	seed(t, database)
	return database
}

func seed(t *testing.T, database string) {
	t.Helper()

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
}

// runnerFor assembles a runner over both participant roles and closes it when
// the test ends, before the scratch database is dropped: cleanups run in
// reverse, and the database was created first.
func runnerFor(t *testing.T, database string, limits queryrunner.Limits, validator queryrunner.Validator) *queryrunner.Runner {
	t.Helper()

	runner := queryrunner.New(clusterFor(t, database), validator, limits)
	t.Cleanup(runner.Close)
	return runner
}

// clusterFor connects as both participant roles, so a test may run either
// kind of contest against the same database.
func clusterFor(t *testing.T, database string) *queryrunner.Cluster {
	t.Helper()

	cluster, err := queryrunner.NewCluster(
		gamedbtest.DSN(t, gamedb.RoleReader, gamedbtest.ReaderPassword(t), database),
		gamedbtest.DSN(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), database))
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

// anything is a validator that admits every statement unwrapped, for the tests
// about what a statement the real checker would refuse could leave behind on a
// connection. The pool's isolation must not rest on the checker: it is the
// layer underneath it.
type anything struct{}

func (anything) Analyse(sql string, p sqlpolicy.Policy) (sqlpolicy.Statement, error) {
	return sqlpolicy.Statement{Text: sql, Explain: true, Writes: p.Mode == sqlpolicy.ModeReadWrite}, nil
}
