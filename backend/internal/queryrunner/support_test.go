package queryrunner_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

// These tests run against a real game cluster as the participant roles, since
// a deadline, a semaphore and a database have no useful stand-in. Without
// GAME_DB_DSN they skip (see gamedbtest).

func setup(t *testing.T) (*queryrunner.Runner, string) {
	t.Helper()
	return setupWith(t, queryrunner.DefaultLimits(), checker.NewChecker())
}

func setupWith(t *testing.T, limits queryrunner.Limits, validator queryrunner.Validator) (*queryrunner.Runner, string) {
	t.Helper()

	database := seeded(t)
	return runnerFor(t, database, limits, validator), database
}

// seeded creates a scratch database with the shared fixture, granted to both
// participant roles.
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
		// The writer may create its own objects, as a read-write template
		// grants; write access to game tables is granted by the write tests.
		`CREATE SCHEMA work`,
		`GRANT USAGE, CREATE ON SCHEMA work TO `+gamedb.RoleWriter,
	)
}

// runnerFor assembles a runner over both participant roles and closes it
// before the scratch database is dropped (cleanups run in reverse).
func runnerFor(t *testing.T, database string, limits queryrunner.Limits, validator queryrunner.Validator) *queryrunner.Runner {
	t.Helper()

	runner := queryrunner.New(clusterFor(t, database), validator, limits)
	t.Cleanup(runner.Close)
	return runner
}

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

// oneParticipant asks most queries; tests about who asks use `other`.
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

// anything is a validator that admits every statement, so tests can show the
// pool's isolation does not rest on the checker.
type anything struct{}

func (anything) Analyse(sql string, p sqlpolicy.Policy) (sqlpolicy.Statement, error) {
	return sqlpolicy.Statement{Text: sql, Explain: true, Writes: p.Mode == sqlpolicy.ModeReadWrite}, nil
}

// unlimited is the default limits without the per-participant rate.
func unlimited() queryrunner.Limits {
	limits := queryrunner.DefaultLimits()
	limits.PerMinute = 0
	return limits
}

// backend returns the server process serving the runner's connection. It
// needs a runner built with the anything validator.
func backend(t *testing.T, runner *queryrunner.Runner, database string) int32 {
	t.Helper()

	result, err := runner.Run(t.Context(), request(database, `SELECT pg_backend_pid()`))
	if err != nil {
		t.Fatalf("reading the backend pid: %v", err)
	}
	return result.Rows[0][0].(int32)
}

// participantBackends counts the reader role's connections to these
// databases, in any state.
func participantBackends(t *testing.T, databases ...string) int {
	t.Helper()

	var n int
	if err := gamedbtest.Admin(t).QueryRow(t.Context(),
		`SELECT count(*) FROM pg_stat_activity WHERE usename = $1 AND datname = ANY($2)`,
		gamedb.RoleReader, databases).Scan(&n); err != nil {
		t.Fatalf("counting participant backends: %v", err)
	}
	return n
}

// eventually polls until the participant backend count is want and returns
// the last count seen. A closed connection's backend exits a moment later.
func eventually(t *testing.T, want int, databases ...string) int {
	t.Helper()

	var got int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if got = participantBackends(t, databases...); got == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

// recreate makes a database of this name again, hardened and seeded like
// the first.
func recreate(t *testing.T, database string) {
	t.Helper()

	admin := gamedbtest.Admin(t)
	if _, err := admin.Exec(t.Context(), `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(database)); err != nil {
		t.Fatalf("recreating %s: %v", database, err)
	}
	user, password := gamedbtest.AdminCredentials(t)
	conn := gamedbtest.Connect(t, user, password, database)
	if err := gamedb.HardenDatabase(t.Context(), conn); err != nil {
		t.Fatalf("hardening %s: %v", database, err)
	}
	_ = conn.Close(t.Context())
	seed(t, database)
}
