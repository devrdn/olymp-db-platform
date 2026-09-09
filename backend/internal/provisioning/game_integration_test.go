package provisioning_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// The whole chain, across both clusters, on the path a deployment actually
// uses (CLAUDE.md rule 10).
//
// Every other test of this feature stops at a boundary: the domain sees a
// fake cluster, the repository sees no cluster at all, and the handler sees
// neither. What none of them can show is the one thing that matters before a
// deploy — that a script an organiser saves in the core database ends up as a
// real database on the game cluster with their tables in it. `BuildTemplate`
// sat in this codebase for months with no caller precisely because nothing
// ever went end to end.
func TestAScriptSavedInTheCoreDatabaseBecomesARealDatabaseOnTheGameCluster(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})

	saved, err := games.SetScript(t.Context(), uuid.New(), contest.ID, `
		CREATE TABLE guests (id uuid PRIMARY KEY, full_name text NOT NULL);
		CREATE TABLE keycard_events (guest_id uuid REFERENCES guests, door text);
		INSERT INTO guests VALUES (gen_random_uuid(), 'Margot Feilhaber');
	`)
	if err != nil {
		t.Fatalf("save the script: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the build finished as %q: %s", built.Status, built.BuildError)
	}

	// The row says ready...
	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if stored.Status != provisioning.TemplateReady {
		t.Fatalf("the stored game is %q, want ready", stored.Status)
	}

	// ...and the database it names really exists, with the author's tables and
	// their row in it. This is the assertion the whole test is for.
	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	var name string
	if err := conn.QueryRow(t.Context(), `SELECT full_name FROM guests`).Scan(&name); err != nil {
		t.Fatalf("read the built game: %v", err)
	}
	if name != "Margot Feilhaber" {
		t.Fatalf("the built game holds %q", name)
	}

	// And the schema panel can describe it, which is the other half of what a
	// participant sees — read here through the same reader the API uses.
	schema, err := provisioning.NewSchemaReader(repo, cluster).
		Schema(t.Context(), provisioning.Contest{ID: contest.ID, Version: stored.Version}, built.Database)
	if err != nil {
		t.Fatalf("read the schema of the built game: %v", err)
	}
	var sawForeignKey bool
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			if table.Name == "keycard_events" && column.Name == "guest_id" && column.References == "guests" {
				sawForeignKey = true
			}
		}
	}
	if len(schema.Tables) != 2 || !sawForeignKey {
		t.Fatalf("the schema of the built game came back as %+v", schema.Tables)
	}
}

// The same chain, for the script that does not build — the path the review
// found leaking, and the only arrangement where the leak is real: the error
// has to be produced by a real pgx connection to a real cluster before there
// is anything to leak.
//
// Named to share the prefix above so `make test-game-build` runs it: that
// target selects by -run TestAScriptSavedInTheCoreDatabase, and a test of this
// chain that no target runs is the state BuildTemplate was in for months.
func TestAScriptSavedInTheCoreDatabaseThatPostgreSQLRefusesTellsTheOrganiserWhatItSaidAndNothingElse(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	// The author credential is wrong on purpose. This is the failure that used
	// to reach GET /contests/{id}/game verbatim: pgx answers a refused login
	// with a *pgconn.ConnectError naming the role, every address it dialled
	// and the database it asked for — with PostgreSQL's own 28P01 nested
	// inside it, which is what defeats a type test at the far end.
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		"not-the-author-password")
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})
	saved, err := games.SetScript(t.Context(), uuid.New(), contest.ID, `CREATE TABLE guests (id int);`)
	if err != nil {
		t.Fatalf("save the script: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, buildErr := games.Build(t.Context(), time.Minute)
	if buildErr == nil {
		t.Fatal("a cluster that refused the author's login was reported as a clean tick")
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("the build finished as %q, want failed", built.Status)
	}

	// The row as GET /contests/{id}/game reads it, straight out of the core
	// database rather than off the value Build happened to return.
	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if stored.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("the stored build error is %q, want the fixed sentence", stored.BuildError)
	}
	for _, ours := range []string{gamedb.RoleAuthor, "28P01", "failed to connect", "password"} {
		if strings.Contains(stored.BuildError, ours) {
			t.Fatalf("the stored build error names %q: %q", ours, stored.BuildError)
		}
	}
	// And the cause really did survive for the log, or this would be a leak
	// traded for an outage nobody can diagnose.
	if !strings.Contains(buildErr.Error(), gamedb.RoleAuthor) {
		t.Fatalf("the error returned for the log is %v; it has to keep what the organiser no longer gets", buildErr)
	}
}

// The same chain again, for the thing an organiser uploading a dump actually
// hits: a statement PostgreSQL refuses, hundreds of lines into a file.
//
// What used to arrive on their screen was `POSITION: 15` — an offset into a
// statement that was cut out of the file before the server ever saw it, so
// there was no way back to the place in the file. The line has to cross the
// whole path: ScriptReader records it on the Statement, runScript hands it to
// the failure, and it has to survive into game_templates.build_error, which
// is what the console reads and what its viewer parses to jump (CLAUDE.md
// rule 11 — the value that drives a check crosses every boundary it has to).
//
// Named to share the prefix `make test-game-build` selects on, for the
// reason the test above it gives.
func TestAScriptSavedInTheCoreDatabaseThatPostgreSQLRefusesNamesTheLineOfTheFile(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})

	// The refusal is on line 5 of the script as saved: line 1 is the newline
	// after the backtick, so the CREATE TABLE is line 2 and the INSERT
	// naming a table nobody made is line 5.
	saved, err := games.SetScript(t.Context(), uuid.New(), contest.ID, `
		CREATE TABLE guests (id int);
		INSERT INTO guests VALUES (1);

		INSERT INTO suspects VALUES (1);
	`)
	if err != nil {
		t.Fatalf("save the script: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("a script PostgreSQL refused was reported as a fault of ours: %v", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("the build finished as %q, want failed", built.Status)
	}

	// The row as GET /contests/{id}/game reads it — the string the console's
	// viewer parses, not the value Build happened to return.
	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if !strings.HasPrefix(stored.BuildError, "line 5: ") {
		t.Fatalf("the stored build error is %q; the organiser has no way to find line 5 of their file",
			stored.BuildError)
	}
	if !strings.Contains(stored.BuildError, "suspects") {
		t.Fatalf("the stored build error is %q; PostgreSQL's own words are still the useful ones",
			stored.BuildError)
	}
}

// A real streaming build: the reason gamedb.Provisioner.BuildTemplate now
// takes an io.Reader is to run a script one statement (or COPY block) at a
// time instead of holding it all in memory, and the one part of that a fake
// connection cannot prove is that pgconn.PgConn.CopyFrom really accepts what
// gamedb.ScriptReader hands it. Shaped like a small pg_dump on purpose: the
// leading "-- Data for Name: ..." comment block pg_dump always writes before
// a table's COPY, and a \N among the rows — the two things a naive
// strings.Split(";") or a copy-data reader that touched the bytes would get
// wrong first.
func TestAScriptSavedInTheCoreDatabaseWithACOPYBlockBuildsARealTable(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})

	const dump = "CREATE TABLE guests (id int, full_name text);\n" +
		"\n" +
		"--\n" +
		"-- Data for Name: guests; Type: TABLE DATA; Schema: public; Owner: -\n" +
		"--\n" +
		"\n" +
		"COPY public.guests (id, full_name) FROM stdin;\n" +
		"1\tMargot Feilhaber\n" +
		"2\t\\N\n" +
		"\\.\n" +
		"\n"

	saved, err := games.SetScript(t.Context(), uuid.New(), contest.ID, dump)
	if err != nil {
		t.Fatalf("save the script: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the build finished as %q: %s", built.Status, built.BuildError)
	}

	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	rows, err := conn.Query(t.Context(), `SELECT id, full_name FROM guests ORDER BY id`)
	if err != nil {
		t.Fatalf("read the built table: %v", err)
	}
	defer rows.Close()

	type guestRow struct {
		id   int
		name *string
	}
	var got []guestRow
	for rows.Next() {
		var r guestRow
		if err := rows.Scan(&r.id, &r.name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("the COPY block loaded %d rows, want 2: %+v", len(got), got)
	}
	if got[0].name == nil || *got[0].name != "Margot Feilhaber" {
		t.Fatalf("row 1 = %+v, want Margot Feilhaber", got[0])
	}
	if got[1].name != nil {
		t.Fatalf(`row 2 = %+v, want NULL (COPY's own \N)`, got[1])
	}
}

// The same chain again, for the table builder's own way in: a Definition
// saved instead of a script, generated into SQL by Definition.SQL, and run
// through the identical BuildTemplate an editor's script and an uploaded
// dump already go through (finishDefinitionBuild's own doc — there is no
// third path). Two tables and a primary key, so what this proves is not just
// "a CREATE TABLE ran" but that a participant can SELECT the columns and
// types the organiser actually described, with the right ones NOT NULL.
//
// Named to share the prefix `make test-game-build` selects on, the same
// reason the tests above it are.
func TestAScriptSavedInTheCoreDatabaseFromATableBuilderDefinitionBuildsARealDatabaseOnTheGameCluster(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if os.Getenv("GAME_DB_DSN") == "" {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	contest, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)

	user, password := gamedbtest.AdminCredentials(t)
	cluster, err := gamedb.NewProvisioner(gamedbtest.Admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
	if err != nil {
		t.Fatalf("open the game cluster: %v", err)
	}

	games := provisioning.NewGames(repo, cluster, editableContest{})

	definition := provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "name", Type: provisioning.ColumnText},
				{Name: "nickname", Type: provisioning.ColumnText, Nullable: true},
			},
			PrimaryKey: []string{"id"},
		},
		{
			Name: "sightings",
			Columns: []provisioning.ColumnDefinition{
				{Name: "suspect_id", Type: provisioning.ColumnInteger},
				{Name: "seen_at", Type: provisioning.ColumnTimestamp},
			},
		},
	}}

	saved, err := games.SetDefinition(t.Context(), uuid.New(), contest.ID, definition)
	if err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the build finished as %q: %s", built.Status, built.BuildError)
	}

	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if stored.Status != provisioning.TemplateReady {
		t.Fatalf("the stored game is %q, want ready", stored.Status)
	}

	// Both tables exist, empty, and a participant can SELECT them — this task
	// creates tables and leaves them empty on purpose (Definition.SQL's own
	// doc); loading the organiser's own rows is a following task's work.
	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	for _, table := range []string{"suspects", "sightings"} {
		var count int
		if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			t.Fatalf("select from %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows, want 0 (this task builds no data)", table, count)
		}
	}

	// The schema panel describes exactly what the organiser declared: both
	// tables, the right columns, the right nullability, and the primary key
	// enforced (a duplicate id is refused).
	schema, err := provisioning.NewSchemaReader(repo, cluster).
		Schema(t.Context(), provisioning.Contest{ID: contest.ID, Version: stored.Version}, built.Database)
	if err != nil {
		t.Fatalf("read the schema of the built game: %v", err)
	}
	if len(schema.Tables) != 2 {
		t.Fatalf("the schema has %d tables, want 2: %+v", len(schema.Tables), schema.Tables)
	}
	var sawSuspects bool
	for _, table := range schema.Tables {
		if table.Name != "suspects" {
			continue
		}
		sawSuspects = true
		byName := make(map[string]provisioning.Column, len(table.Columns))
		for _, column := range table.Columns {
			byName[column.Name] = column
		}
		if id, ok := byName["id"]; !ok || id.Nullable {
			t.Fatalf("suspects.id = %+v (ok=%v), want a NOT NULL column", id, ok)
		}
		if nickname, ok := byName["nickname"]; !ok || !nickname.Nullable {
			t.Fatalf("suspects.nickname = %+v (ok=%v), want a nullable column", nickname, ok)
		}
	}
	if !sawSuspects {
		t.Fatalf("the schema did not describe suspects: %+v", schema.Tables)
	}

	if _, err := conn.Exec(t.Context(), `INSERT INTO suspects (id, name) VALUES (1, 'Margot Feilhaber')`); err != nil {
		t.Fatalf("insert a row the definition's own NOT NULL columns allow: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO suspects (id, name) VALUES (1, 'Duplicate')`); err == nil {
		t.Fatal("the primary key the definition declared did not stop a duplicate id")
	}
}

// editableContest stands for a draft contest: the gate this test is not about.
type editableContest struct{}

func (editableContest) GameEditable(context.Context, uuid.UUID) (bool, error) { return true, nil }
