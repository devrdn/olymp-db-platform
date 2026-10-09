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
// connection left open on it (so it can serve as a template).
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

// The read-only default is turned off first, to prove that missing GRANTs, not
// the default, stop every write.
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

// PostgreSQL only warns on such a GRANT, so the test checks the privilege, not
// an error.
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

// pg_stat_activity shows other participants' queries (the answers), and
// pg_database names their databases.
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

// Exploring the data's shape is part of the exercise.
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

// Inheritance through CREATE DATABASE … TEMPLATE is the only way hardening
// reaches a participant.
func TestTheHardeningIsInheritedByACopyOfTheTemplate(t *testing.T) {
	// No connection: CREATE DATABASE … TEMPLATE refuses while the source has one.
	template := gameDatabase(t)

	copyName := template + "_copy"
	if _, err := admin(t).Exec(t.Context(),
		`CREATE DATABASE `+sqlpolicy.QuoteIdentifier(copyName)+` TEMPLATE `+sqlpolicy.QuoteIdentifier(template)); err != nil {
		t.Fatalf("copying the template: %v", err)
	}
	// Cleanup runs after t.Context() is cancelled, so it uses gamedbtest.Drop.
	t.Cleanup(func() { gamedbtest.Drop(copyName) })

	reader := connectAs(t, roleReader, testReaderPassword(t), copyName)

	refused(t, reader, `SELECT count(*) FROM pg_database`)
	var rows int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("the copy lost the game data: %v", err)
	}
}

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

// The validator and the REVOKEs share one list; this provokes every entry so
// nothing filters it on the way to the database.
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

// A database created without a TEMPLATE clause copies template1, so hardening
// template1 covers a database nobody remembered to harden.
func TestADatabaseCreatedWithNoTemplateIsHardenedAnyway(t *testing.T) {
	requireCluster(t)

	// The deploy job does the same, idempotently.
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
	if _, err := admin(t).Exec(t.Context(), `CREATE DATABASE `+sqlpolicy.QuoteIdentifier(name)); err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	t.Cleanup(func() { gamedbtest.Drop(name) })

	reader := connectAs(t, roleReader, testReaderPassword(t), name)
	refused(t, reader, `SELECT count(*) FROM pg_database`)
	refused(t, reader, `SELECT count(*) FROM pg_stat_activity`)
}
