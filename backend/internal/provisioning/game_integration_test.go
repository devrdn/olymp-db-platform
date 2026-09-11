package provisioning_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
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
	if !gamedbtest.Configured() {
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
	if !gamedbtest.Configured() {
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
	if !gamedbtest.Configured() {
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
	if !gamedbtest.Configured() {
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
	if !gamedbtest.Configured() {
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
				// The three types no CREATE TABLE in a test ever emitted:
				// their keywords were checked against a Go string and never
				// against the parser that has to accept them.
				{Name: "distance_km", Type: provisioning.ColumnNumeric},
				{Name: "seen_on", Type: provisioning.ColumnDate},
				{Name: "confirmed", Type: provisioning.ColumnBoolean},
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

// The same chain again, for this task's own work: a table's own CSV rows,
// loaded through gamedb.Provisioner.LoadTableData's real COPY ... FROM
// STDIN, the way an uploaded dump's own rows already go through runScript
// (CLAUDE.md rule 10 — the path this task's brief names as already proven at
// 1.2 million rows, not a second one written for row-at-a-time INSERTs).
//
// Two tables and every one of the four capabilities the brief lists: a
// chunked CSV upload for suspects, two rows typed in one at a time for
// sightings, one of them then deleted. What this proves is not merely "COPY
// ran" — the fake-cluster tests in tabledata_test.go already show that
// without a database — but that a participant's own SELECT sees exactly the
// rows survived through all three paths and no others: the deleted
// sighting's own suspect has nothing joined to it once this runs for real.
//
// Named to share the prefix `make test-game-build` selects on, the same
// reason the tests above it are.
func TestAScriptSavedInTheCoreDatabaseFromATableBuilderDefinitionWithCSVDataBuildsRowsOnTheGameCluster(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if !gamedbtest.Configured() {
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

	limits := gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 4 << 20, MaxChunkBytes: 256 << 10}
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}
	games := provisioning.NewGames(repo, cluster, editableContest{}).WithTableData(files, limits)

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
				// The three types no test used to carry as far as a real
				// COPY. Each was checked only against a Go parser and a Go
				// string, and the whole point of that check is to agree with
				// the column PostgreSQL actually creates: a value this
				// platform accepts and COPY refuses is minutes of build time
				// spent to produce a failed game.
				{Name: "distance_km", Type: provisioning.ColumnNumeric},
				{Name: "seen_on", Type: provisioning.ColumnDate},
				{Name: "confirmed", Type: provisioning.ColumnBoolean},
			},
		},
	}}
	saved, err := games.SetDefinition(t.Context(), uuid.New(), contest.ID, definition)
	if err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	t.Cleanup(func() { gamedbtest.Drop(saved.Database) })

	// suspects: a whole CSV, uploaded in chunks — the same way a dump is.
	// Deliberately with no trailing newline, which is what a good many
	// exporters write and what validateTableFile accepts: the row added from
	// the form below has to become a row of its own on the end of it, not two
	// rows glued into one line of six fields (which is `extra data after last
	// expected column` from COPY, and a failed build for the whole game).
	const suspectsCSV = "id,name,nickname\n1,Margot Feilhaber,\n2,Duplicate Suspect,Sparrow"
	upload, err := games.BeginTableUpload(t.Context(), contest.ID, "suspects", int64(len(suspectsCSV)))
	if err != nil {
		t.Fatalf("begin table upload: %v", err)
	}
	if _, err := games.AppendTableChunk(t.Context(), contest.ID, upload.ID, 0, strings.NewReader(suspectsCSV)); err != nil {
		t.Fatalf("append table chunk: %v", err)
	}
	if _, err := games.CompleteTableUpload(t.Context(), uuid.New(), contest.ID, upload.ID); err != nil {
		t.Fatalf("complete table upload: %v", err)
	}

	// One more suspect, typed into the form rather than uploaded, with the
	// nullable nickname left empty. An empty value is a NULL on this path
	// (AppendTableRow validates it as one and refuses it in a NOT NULL
	// column), so it has to reach PostgreSQL as one: written as `""` it is the
	// empty string instead, and the IS NULL a task asks about finds nothing.
	if _, err := games.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"3", "Typed In", ""}); err != nil {
		t.Fatalf("append a suspect from the form: %v", err)
	}

	// sightings: two rows typed in one at a time, the first of which is then
	// deleted — the row from a form and the tombstone, on the identical file.
	//
	// The numeric values are deliberately the two forms validNumericLiteral's
	// own doc says were "verified against a live PostgreSQL 16 instance" and
	// nothing re-verified since: a digit separator and one of the type's
	// special values. If either were wrong, COPY is where it would show, and
	// this is that check running by itself rather than by hand.
	if _, err := games.AppendTableRow(t.Context(), uuid.New(), contest.ID, "sightings",
		[]string{"1", "2024-01-01 10:00:00", "1_000.5", "2024-01-01", "yes"}); err != nil {
		t.Fatalf("append sighting 1: %v", err)
	}
	if _, err := games.AppendTableRow(t.Context(), uuid.New(), contest.ID, "sightings",
		[]string{"2", "2024-01-02 11:00:00", "NaN", "2024-01-02", "f"}); err != nil {
		t.Fatalf("append sighting 2: %v", err)
	}
	if err := games.DeleteTableRow(t.Context(), uuid.New(), contest.ID, "sightings", 1); err != nil {
		t.Fatalf("delete sighting 1: %v", err)
	}

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the build finished as %q: %s", built.Status, built.BuildError)
	}

	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	var suspectCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspectCount); err != nil {
		t.Fatalf("count suspects: %v", err)
	}
	if suspectCount != 3 {
		t.Fatalf("suspects has %d rows, want 3 (the whole uploaded CSV, plus the row from the form)", suspectCount)
	}

	// Both empty nicknames are NULL in the database: the one that arrived as a
	// bare empty field in the uploaded CSV, and the one the form left empty.
	var nullNicknames int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM suspects WHERE nickname IS NULL`).Scan(&nullNicknames); err != nil {
		t.Fatalf("count null nicknames: %v", err)
	}
	if nullNicknames != 2 {
		t.Fatalf("%d suspect(s) have a NULL nickname, want 2 — an empty value must not be stored as an empty string", nullNicknames)
	}

	// Only the surviving sighting is there to join: the tombstoned row 1
	// (suspect 1, 2024-01-01) never reached the database at all.
	rows, err := conn.Query(t.Context(),
		`SELECT s.id, s.name, si.seen_at FROM sightings si JOIN suspects s ON s.id = si.suspect_id ORDER BY si.seen_at`)
	if err != nil {
		t.Fatalf("join suspects and sightings: %v", err)
	}
	defer rows.Close()

	type joined struct {
		id   int
		name string
	}
	var got []joined
	for rows.Next() {
		var j joined
		var seenAt time.Time
		if err := rows.Scan(&j.id, &j.name, &seenAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, j)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("the join returned %d row(s), want exactly 1: %+v", len(got), got)
	}
	if got[0].id != 2 || got[0].name != "Duplicate Suspect" {
		t.Fatalf("the surviving sighting joined to %+v, want suspect 2", got[0])
	}

	// The three types this test used to stop short of: what COPY put in the
	// columns has to be a numeric, a date and a boolean — not text that
	// happens to look like them. Asked as the types themselves (`= 'NaN'`,
	// `= date '...'`, `IS false`) so a column of the wrong type could not
	// answer at all.
	var typed bool
	if err := conn.QueryRow(t.Context(), `
		SELECT distance_km = 'NaN'::numeric AND seen_on = date '2024-01-02' AND confirmed IS false
		FROM sightings`).Scan(&typed); err != nil {
		t.Fatalf("read the numeric, date and boolean columns back: %v", err)
	}
	if !typed {
		t.Fatal("the numeric, date or boolean column did not come back as the value that was uploaded")
	}
}

// editableContest stands for a draft contest: the gate this test is not about.
type editableContest struct{}

func (editableContest) GameEditable(context.Context, uuid.UUID) (bool, error) { return true, nil }

// The value check this platform makes before a build, held against the
// database that actually decides.
//
// validateScalar exists for one reason: refuse a value *before* a build
// spends minutes and dies inside COPY. That is a claim about PostgreSQL, and
// until this ran it was a claim checked by hand — validNumericLiteral's own
// doc says its rules were "verified against a live PostgreSQL 16 instance",
// which is a sentence, not a test. Nothing re-verified it on an upgrade, and
// nothing would have noticed the day the two stopped agreeing.
//
// Two directions, and they are not symmetric:
//
//   - Everything this platform accepts, PostgreSQL must accept. A value
//     waved through here and refused by COPY is exactly the failed build the
//     pre-check exists to prevent, so this holds for every candidate below.
//   - Where the doc names a form as refused by PostgreSQL itself — the
//     underscore rules, the hexadecimal float literal strconv.ParseFloat
//     would have taken — PostgreSQL must refuse it too. Elsewhere this
//     platform is allowed to be the stricter of the two (it insists on
//     YYYY-MM-DD where PostgreSQL would also read 01/02/2024), and that is a
//     deliberate narrowing rather than a disagreement.
//
// The domain's own answer is taken from AppendTableRow — the path an
// organiser's form actually takes — rather than from the unexported checker,
// so what is compared is what a request would get.
//
// Named to share the prefix `make test-game-build` selects on.
func TestAScriptSavedInTheCoreDatabaseAgreesWithPostgreSQLAboutEveryValueForm(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-game-build`")
	}
	if !gamedbtest.Configured() {
		t.Skip("GAME_DB_DSN is not set; run `make test-game-build`")
	}

	user, password := gamedbtest.AdminCredentials(t)
	conn := gamedbtest.Connect(t, user, password, "postgres")
	defer func() { _ = conn.Close(context.Background()) }()

	limits := gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 4 << 20, MaxChunkBytes: 256 << 10}
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("open the table data store: %v", err)
	}

	// One single-column table per type, so a value refused for one column
	// never blocks another type's own candidates.
	types := map[string]provisioning.ColumnType{
		"integers":   provisioning.ColumnInteger,
		"numerics":   provisioning.ColumnNumeric,
		"dates":      provisioning.ColumnDate,
		"stamps":     provisioning.ColumnTimestamp,
		"flags":      provisioning.ColumnBoolean,
		"free_texts": provisioning.ColumnText,
	}
	tables := make([]provisioning.TableDefinition, 0, len(types))
	for name, typ := range types {
		tables = append(tables, provisioning.TableDefinition{
			Name:    name,
			Columns: []provisioning.ColumnDefinition{{Name: "v", Type: typ}},
		})
	}
	// Definition.SQL is deterministic in the order it is given, and the order
	// a map hands these back is not — sorted so a failure names the same
	// table every run.
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	pgTypes := map[provisioning.ColumnType]string{
		provisioning.ColumnInteger:   "integer",
		provisioning.ColumnNumeric:   "numeric",
		provisioning.ColumnDate:      "date",
		provisioning.ColumnTimestamp: "timestamp without time zone",
		provisioning.ColumnBoolean:   "boolean",
		provisioning.ColumnText:      "text",
	}

	contest, _ := contestFor(t, t.Context(), 0)
	games := provisioning.NewGames(postgres.NewGameInstances(testPool), &buildCluster{}, editableContest{}).
		WithTableData(files, limits)
	if _, err := games.SetDefinition(t.Context(), uuid.New(), contest.ID,
		provisioning.Definition{Tables: tables}); err != nil {
		t.Fatalf("save the definition: %v", err)
	}

	for _, c := range valueFormCandidates() {
		t.Run(c.table+"/"+c.value, func(t *testing.T) {
			_, domainErr := games.AppendTableRow(t.Context(), uuid.New(), contest.ID, c.table, []string{c.value})
			domainTakesIt := domainErr == nil
			if !domainTakesIt && !errors.Is(domainErr, provisioning.ErrTableValueInvalid) {
				t.Fatalf("AppendTableRow(%q) = %v, want either success or ErrTableValueInvalid", c.value, domainErr)
			}

			var scratch string
			castErr := conn.QueryRow(t.Context(),
				"SELECT $1::"+pgTypes[types[c.table]]+"::text", c.value).Scan(&scratch)
			clusterTakesIt := castErr == nil

			if domainTakesIt && !clusterTakesIt {
				t.Fatalf("this platform accepts %q for a %s column and PostgreSQL refuses it (%v) — "+
					"that is a build that dies inside COPY", c.value, types[c.table], castErr)
			}
			if c.alsoRefusedByPostgres {
				if domainTakesIt {
					t.Fatalf("%q is documented as refused and this platform accepted it", c.value)
				}
				if clusterTakesIt {
					t.Fatalf("%q is documented as refused by PostgreSQL itself, and PostgreSQL took it: "+
						"the reasoning in validNumericLiteral's own doc no longer holds", c.value)
				}
			}
		})
	}
}

// valueFormCandidate is one value offered to one single-column table.
// alsoRefusedByPostgres marks the forms whose refusal this platform's own
// comments attribute to PostgreSQL rather than to a narrowing of its own.
type valueFormCandidate struct {
	table                 string
	value                 string
	alsoRefusedByPostgres bool
}

func valueFormCandidates() []valueFormCandidate {
	accepted := func(table string, values ...string) []valueFormCandidate {
		out := make([]valueFormCandidate, 0, len(values))
		for _, v := range values {
			out = append(out, valueFormCandidate{table: table, value: v})
		}
		return out
	}
	refusedByBoth := func(table string, values ...string) []valueFormCandidate {
		out := make([]valueFormCandidate, 0, len(values))
		for _, v := range values {
			out = append(out, valueFormCandidate{table: table, value: v, alsoRefusedByPostgres: true})
		}
		return out
	}

	var all []valueFormCandidate
	all = append(all, accepted("integers", "0", "-1", "2147483647", "-2147483648")...)
	all = append(all, refusedByBoth("integers", "1.5", "abc", "2147483648")...)

	// numeric is the type validNumericLiteral's own doc makes claims about:
	// the digit separator, the special values, and the two things
	// strconv.ParseFloat would have got wrong (an exponent numeric holds
	// happily, and a hexadecimal literal numeric_in has never accepted).
	all = append(all, accepted("numerics",
		"5", "5.", ".5", "5.5", "-1.5e10", "1_000", "1e400",
		"NaN", "nan", "Infinity", "-Infinity", "+Inf", "  7  ")...)
	all = append(all, refusedByBoth("numerics",
		"1__000", "_1000", "1000_", "0x1p-2", "+NaN", "-NaN", "1e", "5..5", "abc")...)

	all = append(all, accepted("dates", "2024-01-01", "0001-01-01", "9999-12-31")...)
	all = append(all, refusedByBoth("dates", "2024-02-30", "not-a-date")...)
	// Refused here and read by PostgreSQL: this platform insists on one
	// spelling so that what an organiser sees in the window is what a
	// participant queries, which is a narrowing rather than a disagreement —
	// so it is not marked alsoRefusedByPostgres.
	all = append(all, valueFormCandidate{table: "dates", value: "01/02/2024"})

	all = append(all, accepted("stamps", "2024-01-01 10:00:00", "2024-01-01 10:00")...)
	all = append(all, refusedByBoth("stamps", "2024-01-01 25:00:00", "yesterday afternoon")...)

	all = append(all, accepted("flags", "true", "false", "t", "f", "yes", "no", "y", "n", "1", "0")...)
	all = append(all, refusedByBoth("flags", "maybe", "2")...)

	// text takes anything that is not empty (an empty field is a NULL, which
	// is a different rule and has its own test).
	all = append(all, accepted("free_texts", "anything at all", "0x1p-2", "NaN")...)
	return all
}
