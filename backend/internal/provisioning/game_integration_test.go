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

// The whole chain across both clusters (CLAUDE.md rule 10): every other test
// stops at a fake cluster or none. Tests in this file share the prefix
// `make test-game-build` selects with -run.
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

	stored, err := repo.Template(t.Context(), contest.ID)
	if err != nil {
		t.Fatalf("read the game back: %v", err)
	}
	if stored.Status != provisioning.TemplateReady {
		t.Fatalf("the stored game is %q, want ready", stored.Status)
	}

	// The database it names exists and holds the author's row.
	conn := gamedbtest.Connect(t, user, password, built.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	var name string
	if err := conn.QueryRow(t.Context(), `SELECT full_name FROM guests`).Scan(&name); err != nil {
		t.Fatalf("read the built game: %v", err)
	}
	if name != "Margot Feilhaber" {
		t.Fatalf("the built game holds %q", name)
	}

	// The schema panel describes it through the reader the API uses.
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

// Only a real pgx connection produces the error text that could leak.
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
	// A wrong author password: pgx's *pgconn.ConnectError names the role, the
	// addresses and the database, with 28P01 nested inside it.
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

	// Read the row as the API serves it, not the value Build returned.
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
	// The cause must still reach the log.
	if !strings.Contains(buildErr.Error(), gamedb.RoleAuthor) {
		t.Fatalf("the error returned for the log is %v; it has to keep what the organiser no longer gets", buildErr)
	}
}

// PostgreSQL's POSITION is an offset into one statement, useless in a long
// file. The line number must travel from ScriptReader through runScript into
// build_error, which the console parses to jump (CLAUDE.md rule 11).
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

	// Line 1 is the newline after the backtick, so the bad INSERT is line 5.
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

	// Read the row the console parses, not the value Build returned.
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

// Only a real connection proves pgconn's CopyFrom accepts what
// gamedb.ScriptReader streams. The input mimics pg_dump: a comment block
// before the COPY and a \N among the rows.
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

// The built database has the columns, nullability and primary key the
// definition declared.
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
				// Checks these type keywords against the real parser.
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

	// No table data was uploaded, so both tables are empty.
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

// Table data loaded through LoadTableData's real COPY (CLAUDE.md rule 10): a
// chunked CSV upload, rows typed into the form, and a deleted row. A
// participant's SELECT must see exactly the surviving rows.
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
				// The Go value checks for these types must agree with COPY,
				// or an accepted value fails the whole build.
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

	// The first build runs right after SetDefinition, before any row exists,
	// as the background job does on a deployment.
	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build (first, right after the definition was saved): %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("the first build finished as %q: %s", built.Status, built.BuildError)
	}
	// Build claims the oldest pending template of any contest, so a concurrent
	// test's could be claimed instead; fail loudly if so.
	if built.ContestID != contest.ID {
		t.Fatalf("the first build claimed contest %s, not this test's own %s — a concurrent test's template was claimed instead", built.ContestID, contest.ID)
	}

	// Empty after the first build, which proves the rows asserted at the end
	// were loaded by the second.
	firstConn := gamedbtest.Connect(t, user, password, built.Database)
	var suspectsBeforeAnyDataWasLoaded int
	if err := firstConn.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspectsBeforeAnyDataWasLoaded); err != nil {
		t.Fatalf("count suspects right after the first build: %v", err)
	}
	if suspectsBeforeAnyDataWasLoaded != 0 {
		t.Fatalf("suspects has %d row(s) right after the first build, want 0 — no row can have been loaded before the build that first named its table ever ran", suspectsBeforeAnyDataWasLoaded)
	}
	_ = firstConn.Close(context.Background())

	// No trailing newline, as many exporters write: the form row appended
	// below must start a line of its own, or COPY fails the whole build.
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

	// An empty form value is a NULL and must reach PostgreSQL as one, not as
	// `""`.
	if _, err := games.AppendTableRow(t.Context(), uuid.New(), contest.ID, "suspects", []string{"3", "Typed In", ""}); err != nil {
		t.Fatalf("append a suspect from the form: %v", err)
	}

	// Two typed rows, the first then deleted. The numerics are a digit
	// separator and a special value, both forms COPY has to accept.
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

	// The data above sits on disk until RequestBuild asks for a rebuild.
	asked, err := games.RequestBuild(t.Context(), uuid.New(), contest.ID)
	if err != nil {
		t.Fatalf("request the build again, now that the table builder's data has changed: %v", err)
	}
	if asked.Version <= built.Version {
		t.Fatalf("RequestBuild returned version %d, want it greater than the first build's %d", asked.Version, built.Version)
	}

	second, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build (second, with the table builder's data on disk): %v", err)
	}
	if second.Status != provisioning.TemplateReady {
		t.Fatalf("the second build finished as %q: %s", second.Status, second.BuildError)
	}
	if second.ContestID != contest.ID {
		t.Fatalf("the second build claimed contest %s, not this test's own %s — a concurrent test's template was claimed instead", second.ContestID, contest.ID)
	}
	// Today the same name as saved.Database (no version in it), registered so
	// a versioned name would not leak a database.
	t.Cleanup(func() { gamedbtest.Drop(second.Database) })

	conn := gamedbtest.Connect(t, user, password, second.Database)
	defer func() { _ = conn.Close(context.Background()) }()

	var suspectCount int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspectCount); err != nil {
		t.Fatalf("count suspects: %v", err)
	}
	if suspectCount != 3 {
		t.Fatalf("suspects has %d rows, want 3 (the whole uploaded CSV, plus the row from the form)", suspectCount)
	}

	// One empty nickname came from the CSV, one from the form.
	var nullNicknames int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM suspects WHERE nickname IS NULL`).Scan(&nullNicknames); err != nil {
		t.Fatalf("count null nicknames: %v", err)
	}
	if nullNicknames != 2 {
		t.Fatalf("%d suspect(s) have a NULL nickname, want 2 — an empty value must not be stored as an empty string", nullNicknames)
	}

	// The deleted sighting (suspect 1) must not be there.
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

	// Compared as typed values, so a column of the wrong type cannot answer.
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

// editableContest stands for a draft contest.
type editableContest struct{}

func (editableContest) GameEditable(context.Context, uuid.UUID) (bool, error) { return true, nil }

// The pre-build value check (validateScalar) must agree with PostgreSQL, or a
// build dies inside COPY. Everything it accepts PostgreSQL must accept; a form
// the code says PostgreSQL refuses must be refused by both. Elsewhere the
// platform may be stricter (YYYY-MM-DD only). Its answer is taken from
// AppendTableRow, the path a request takes.
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

	// One single-column table per type, so candidates do not block each other.
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
	// Sorted so a failure names the same table every run.
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
// alsoRefusedByPostgres marks forms PostgreSQL itself refuses, as opposed to
// the platform's own narrowing.
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

	// Digit separators, special values, and two forms strconv.ParseFloat gets
	// wrong: a huge exponent numeric holds and a hex literal numeric_in refuses.
	all = append(all, accepted("numerics",
		"5", "5.", ".5", "5.5", "-1.5e10", "1_000", "1e400",
		"NaN", "nan", "Infinity", "-Infinity", "+Inf", "  7  ")...)
	all = append(all, refusedByBoth("numerics",
		"1__000", "_1000", "1000_", "0x1p-2", "+NaN", "-NaN", "1e", "5..5", "abc")...)

	all = append(all, accepted("dates", "2024-01-01", "0001-01-01", "9999-12-31")...)
	all = append(all, refusedByBoth("dates", "2024-02-30", "not-a-date")...)
	// Refused here but read by PostgreSQL: one spelling keeps what the
	// organiser sees equal to what a participant queries.
	all = append(all, valueFormCandidate{table: "dates", value: "01/02/2024"})

	all = append(all, accepted("stamps", "2024-01-01 10:00:00", "2024-01-01 10:00")...)
	all = append(all, refusedByBoth("stamps", "2024-01-01 25:00:00", "yesterday afternoon")...)

	all = append(all, accepted("flags", "true", "false", "t", "f", "yes", "no", "y", "n", "1", "0")...)
	all = append(all, refusedByBoth("flags", "maybe", "2")...)

	// An empty text field is a NULL, tested elsewhere.
	all = append(all, accepted("free_texts", "anything at all", "0x1p-2", "NaN")...)
	return all
}
