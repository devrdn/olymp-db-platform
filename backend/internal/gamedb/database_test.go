package gamedb_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
)

// gameDatabase returns a hardened database holding a small game, with no
// connection left open on it.
func gameDatabase(t *testing.T) string {
	t.Helper()

	database := scratchDatabase(t)
	asOwner(t, database,
		`CREATE TABLE evidence (id int PRIMARY KEY, note text)`,
		`INSERT INTO evidence VALUES (1, 'a knife'), (2, 'a letter')`,
		`GRANT USAGE ON SCHEMA public TO `+roleReader,
		`GRANT SELECT ON ALL TABLES IN SCHEMA public TO `+roleReader,
	)
	return database
}

// setupGame is gameDatabase plus a connection as the participant's role.
func setupGame(t *testing.T) (*pgx.Conn, string) {
	t.Helper()

	database := gameDatabase(t)
	return connectAs(t, roleReader, testReaderPassword(t), database), database
}

func TestTheReaderCanReadTheGame(t *testing.T) {
	reader, _ := setupGame(t)

	var rows int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("the reader cannot read the game: %v", err)
	}
	if rows != 2 {
		t.Fatalf("count = %d, want 2", rows)
	}
}

// The guarantee the whole design rests on: even a query that reached the
// database unchecked cannot write. Each statement below runs *after* the
// session has turned the read-only default off, because that default is not
// what stops it — the absence of a GRANT is, and this test exists to prove
// which of the two is load-bearing.
func TestNoWriteSurvivesEvenWithTheReadOnlyDefaultOff(t *testing.T) {
	reader, _ := setupGame(t)

	if _, err := reader.Exec(t.Context(), `SET default_transaction_read_only = off`); err != nil {
		t.Fatalf("could not turn the default off: %v", err)
	}

	for _, statement := range []string{
		`INSERT INTO evidence VALUES (3, 'a lie')`,
		`UPDATE evidence SET note = 'edited'`,
		`DELETE FROM evidence`,
		`TRUNCATE evidence`,
		`DROP TABLE evidence`,
		`ALTER TABLE evidence ADD COLUMN planted text`,
		`CREATE TABLE mine (x int)`,
		`CREATE VIEW mine AS SELECT 1`,
		`CREATE INDEX ON evidence (note)`,
	} {
		t.Run(statement, func(t *testing.T) {
			err := refused(t, reader, statement)
			if !strings.Contains(err.Error(), "permission denied") &&
				!strings.Contains(err.Error(), "must be owner") {
				t.Fatalf("refused, but not by privileges: %v", err)
			}
		})
	}
}

// Granting itself more is not an error — PostgreSQL warns and grants nothing —
// so the assertion has to be about the privilege rather than about the
// statement. A test that only checked for an error here would have passed
// while believing something it had not shown.
func TestTheReaderCannotGrantItselfMore(t *testing.T) {
	reader, _ := setupGame(t)

	if _, err := reader.Exec(t.Context(), `GRANT ALL ON evidence TO `+roleReader); err != nil {
		t.Logf("the grant errored outright: %v", err)
	}

	var granted bool
	if err := reader.QueryRow(t.Context(),
		`SELECT has_table_privilege($1, 'evidence', 'INSERT')`, roleReader).Scan(&granted); err != nil {
		t.Fatalf("asking about the privilege: %v", err)
	}
	if granted {
		t.Fatal("the reader granted itself INSERT")
	}
}

// pg_stat_activity shows the queries other participants are running, which
// during an olympiad is simply the answers. pg_database names everyone else's
// database. Neither is the participant's game.
func TestTheSensitiveCatalogsAreRefusedByTheDatabaseItself(t *testing.T) {
	reader, _ := setupGame(t)

	for _, statement := range []string{
		`SELECT count(*) FROM pg_database`,
		`SELECT count(*) FROM pg_stat_activity`,
		`SELECT count(*) FROM pg_roles`,
		`SELECT count(*) FROM pg_user`,
		`SELECT count(*) FROM pg_settings`,
		`SELECT count(*) FROM pg_stat_replication`,
		`SELECT count(*) FROM pg_shadow`,
		`SELECT count(*) FROM pg_authid`,
	} {
		t.Run(statement, func(t *testing.T) { refused(t, reader, statement) })
	}
}

// The other half of the same decision: looking at the shape of the data is
// part of the exercise, so the structural catalogs stay readable. A test for
// each half, because a REVOKE that took both would be a contest nobody can
// explore and would otherwise be noticed only by a participant.
func TestTheStructuralCatalogsStayReadable(t *testing.T) {
	reader, _ := setupGame(t)

	for _, statement := range []string{
		`SELECT count(*) FROM pg_class`,
		`SELECT count(*) FROM pg_attribute`,
		`SELECT count(*) FROM pg_namespace`,
		`SELECT count(*) FROM information_schema.tables`,
		`SELECT count(*) FROM information_schema.columns`,
	} {
		t.Run(statement, func(t *testing.T) {
			if _, err := reader.Exec(t.Context(), statement); err != nil {
				t.Fatalf("a structural catalog is not readable: %v", err)
			}
		})
	}
}

// The hardening has to survive CREATE DATABASE … TEMPLATE, because that is the
// only way it reaches a participant: it is applied once when the template is
// built and inherited by every copy. If it did not carry, every instance would
// be unhardened and nothing would say so.
func TestTheHardeningIsInheritedByACopyOfTheTemplate(t *testing.T) {
	// No reader connection here on purpose: CREATE DATABASE … TEMPLATE refuses
	// while anything is connected to the source, which is exactly the template
	// discipline section 4.2 requires of the provisioner.
	template := gameDatabase(t)

	copyName := template + "_copy"
	if _, err := admin(t).Exec(t.Context(),
		`CREATE DATABASE `+sqlpolicy.QuoteIdentifier(copyName)+` TEMPLATE `+sqlpolicy.QuoteIdentifier(template)); err != nil {
		t.Fatalf("copying the template: %v", err)
	}
	// gamedbtest.Drop rather than a pool call: cleanup runs after the test's
	// own context is cancelled, and anything taking t.Context() here fails for
	// a reason that has nothing to do with the test.
	t.Cleanup(func() { gamedbtest.Drop(copyName) })

	reader := connectAs(t, roleReader, testReaderPassword(t), copyName)

	refused(t, reader, `SELECT count(*) FROM pg_database`)
	var rows int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("the copy lost the game data: %v", err)
	}
}

// A participant's role has no business anywhere but a game database. It cannot
// separate one participant from another — they share the role, and what keeps
// them apart is that the Query Runner chooses the database name from
// game_instances, never the client — but it does keep the role out of the
// cluster's own databases.
func TestTheParticipantRoleCannotReachTheMaintenanceDatabases(t *testing.T) {
	requireCluster(t)

	var maintenance string
	if err := admin(t).QueryRow(t.Context(), `SELECT current_database()`).Scan(&maintenance); err != nil {
		t.Fatalf("asking which database the provisioner uses: %v", err)
	}

	for _, database := range []string{"postgres", "template1", maintenance} {
		t.Run(database, func(t *testing.T) {
			err := tryConnectAs(t, roleReader, testReaderPassword(t), database)
			if err == nil {
				t.Fatalf("the reader connected to %s", database)
			}
			if !strings.Contains(err.Error(), "CONNECT") && !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("refused for an unexpected reason: %v", err)
			}
		})
	}
}

// The one description, checked in both directions.
//
// The validator refuses these by name and the database revokes them by name,
// and the architecture's claim is that the two cannot drift because they are
// built from one list. That is only true while nothing filters the list on the
// way to the REVOKEs, so this walks the exported list itself and provokes each
// entry.
func TestEverySensitiveCatalogTheValidatorNamesIsAlsoRevoked(t *testing.T) {
	reader, _ := setupGame(t)

	names := sqlpolicy.SensitiveCatalogs()
	if len(names) == 0 {
		t.Fatal("the shared list is empty")
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var present bool
			if err := reader.QueryRow(t.Context(),
				`SELECT EXISTS (
					SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
					WHERE n.nspname = 'pg_catalog' AND c.relname = $1)`, name).Scan(&present); err != nil {
				t.Fatalf("looking for the relation: %v", err)
			}
			if !present {
				t.Skipf("pg_catalog.%s does not exist on this server", name)
			}
			refused(t, reader, `SELECT 1 FROM pg_catalog.`+sqlpolicy.QuoteIdentifier(name)+` LIMIT 1`)
		})
	}
}

// Hardening reaches a participant by being inherited, and the surest way to
// inherit it is to make the default carry it: every database created without
// an explicit template comes from template1. Hardening that once means a
// database nobody remembered to harden is hardened anyway — which matters
// because the failure mode of the alternative is silent, an instance that
// looks exactly like the others and is not.
func TestADatabaseCreatedWithNoTemplateIsHardenedAnyway(t *testing.T) {
	requireCluster(t)

	// What the deploy job does, and idempotent, so running it here is running
	// the real thing rather than a rehearsal of it.
	template := connectAsOwner(t, "template1")
	if err := gamedb.HardenDatabase(t.Context(), template); err != nil {
		t.Fatalf("hardening template1: %v", err)
	}
	// CREATE DATABASE refuses while anything is connected to its source.
	if err := template.Close(t.Context()); err != nil {
		t.Fatalf("closing template1: %v", err)
	}

	name := "gamedb_default_" + strings.ReplaceAll(t.Name(), "/", "_")
	if _, err := admin(t).Exec(t.Context(),
		`DROP DATABASE IF EXISTS `+sqlpolicy.QuoteIdentifier(name)+` WITH (FORCE)`); err != nil {
		t.Fatalf("clearing a previous run: %v", err)
	}
	// No TEMPLATE clause at all: this is the shape a person types.
	if _, err := admin(t).Exec(t.Context(), `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(name)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() { gamedbtest.Drop(name) })

	reader := connectAs(t, roleReader, testReaderPassword(t), name)
	refused(t, reader, `SELECT count(*) FROM pg_database`)
	refused(t, reader, `SELECT count(*) FROM pg_stat_activity`)
}
