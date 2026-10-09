package gamedb_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A small schema and some data, as an author would upload it.
const detectiveScript = `
CREATE TABLE suspects (id serial PRIMARY KEY, name text NOT NULL, city text);
CREATE TABLE evidence (id serial PRIMARY KEY, note text);
INSERT INTO suspects (name, city) VALUES ('Ionescu', 'Chisinau'), ('Popescu', 'Balti');
INSERT INTO evidence (note) VALUES ('a knife'), ('a letter');
CREATE VIEW recent AS SELECT * FROM evidence;
`

func provisioner(t *testing.T) *gamedb.Provisioner {
	t.Helper()
	requireCluster(t)

	user, password := gamedbtest.AdminCredentials(t)
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}
	return p
}

func named(t *testing.T, suffix string) string {
	t.Helper()

	// PostgreSQL stops at 63 characters. The suffix goes on after trimming,
	// or two names in one test could trim to the same database.
	base := strings.ToLower("gamedb_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	if room := 55 - len(suffix); len(base) > room {
		base = base[:room]
	}
	name := base + "_" + suffix
	gamedbtest.Drop(name)
	t.Cleanup(func() { gamedbtest.Drop(name) })
	return name
}

func buildTemplate(t *testing.T, policy sqlpolicy.Policy) (*gamedb.Provisioner, string, sqlpolicy.Policy) {
	t.Helper()

	p := provisioner(t)
	template := named(t, "tpl")
	if err := buildTemplateString(p, t.Context(), template, detectiveScript, policy); err != nil {
		t.Fatalf("building the template: %v", err)
	}
	return p, template, policy
}

func TestATemplateHoldsTheAuthorsSchemaAndData(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	var suspects int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspects); err != nil {
		t.Fatalf("a participant cannot read the game: %v", err)
	}
	if suspects != 2 {
		t.Fatalf("suspects = %d, want 2", suspects)
	}
}

func TestTwoInstancesDoNotShareData(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	first, second := named(t, "one"), named(t, "two")
	for _, name := range []string{first, second} {
		if err := p.CreateInstance(t.Context(), template, name, policy); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), first)
	if _, err := writer.Exec(t.Context(), `INSERT INTO evidence (note) VALUES ('planted')`); err != nil {
		t.Fatalf("the writer cannot write to a writable table: %v", err)
	}

	other := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), second)
	var rows int
	if err := other.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("reading the second instance: %v", err)
	}
	if rows != 2 {
		t.Fatalf("the second instance sees %d rows; one participant's write reached another", rows)
	}
}

// The template's privileges must hold even if the validator does not.
func TestAReadOnlyTemplateGrantsNothingThatWrites(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	if _, err := reader.Exec(t.Context(), `SET default_transaction_read_only = off`); err != nil {
		t.Fatalf("could not turn the default off: %v", err)
	}

	for _, statement := range []string{
		`INSERT INTO evidence (note) VALUES ('planted')`,
		`UPDATE suspects SET city = 'nowhere'`,
		`DELETE FROM evidence`,
		`CREATE TABLE work.mine (x int)`,
		`CREATE VIEW work.mine AS SELECT 1`,
	} {
		t.Run(statement, func(t *testing.T) { refused(t, reader, statement) })
	}
}

// A template built before TRUNCATE joined the policy's grants must still
// yield instances that can truncate: it is how a participant gets out of
// the disk quota.
func TestAnInstanceMayTruncateAlthoughItsTemplateCouldNot(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))
	asOwner(t, template, `REVOKE TRUNCATE ON evidence FROM `+gamedb.RoleWriter)

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)
	if _, err := writer.Exec(t.Context(), `TRUNCATE evidence`); err != nil {
		t.Fatalf("the writable table could not be emptied: %v", err)
	}
	// Settling grants only what the policy names.
	refused(t, writer, `TRUNCATE suspects`)
}

// In a contest with no writable tables, settling grants nothing.
func TestAReadOnlyInstanceMayNotTruncate(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	if _, err := reader.Exec(t.Context(), `SET default_transaction_read_only = off`); err != nil {
		t.Fatalf("could not turn the default off: %v", err)
	}
	refused(t, reader, `TRUNCATE evidence`)
}

func TestWritingReachesOnlyTheTablesThePolicyNames(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)

	if _, err := writer.Exec(t.Context(), `INSERT INTO evidence (note) VALUES ('a note')`); err != nil {
		t.Fatalf("the named table is not writable: %v", err)
	}
	// suspects is not on the list.
	refused(t, writer, `INSERT INTO suspects (name) VALUES ('someone')`)
	refused(t, writer, `UPDATE suspects SET city = 'nowhere'`)
	refused(t, writer, `DROP TABLE evidence`)
}

func TestOwnObjectsLiveInWorkAndNowhereElse(t *testing.T) {
	policy := sqlpolicy.ReadWrite()
	policy.AllowOwnTables = true
	policy.AllowCreateView = true
	p, template, policy := buildTemplate(t, policy)

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)

	if _, err := writer.Exec(t.Context(), `CREATE TABLE work.notes (x int)`); err != nil {
		t.Fatalf("their own schema is not writable: %v", err)
	}
	if _, err := writer.Exec(t.Context(), `CREATE VIEW work.clues AS SELECT * FROM evidence`); err != nil {
		t.Fatalf("a view in their own schema was refused: %v", err)
	}
	refused(t, writer, `CREATE TABLE public.mine (x int)`)
}

func TestAnInstanceInheritsTheCatalogueRules(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	refused(t, reader, `SELECT count(*) FROM pg_database`)
	refused(t, reader, `SELECT count(*) FROM pg_stat_activity`)
}

// A template with a connection cannot be copied; keeping it free is the
// provisioner's job.
func TestATemplateCanBeRebuiltAndCopiedStraightAfter(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	if err := buildTemplateString(p, t.Context(), template,
		`CREATE TABLE suspects (id int); INSERT INTO suspects VALUES (7);`, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("rebuilding: %v", err)
	}

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("copying straight after a rebuild: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	var only int
	if err := reader.QueryRow(t.Context(), `SELECT id FROM suspects`).Scan(&only); err != nil {
		t.Fatalf("the rebuilt data is not there: %v", err)
	}
	if only != 7 {
		t.Fatalf("id = %d; the instance came from the old template", only)
	}
}

func TestResettingReplacesTheInstanceUnderALiveConnection(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)
	if _, err := writer.Exec(t.Context(), `DELETE FROM evidence`); err != nil {
		t.Fatalf("emptying the table: %v", err)
	}

	// Their connection is still open, on purpose.
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	fresh := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)
	var rows int
	if err := fresh.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("reading the reset instance: %v", err)
	}
	if rows != 2 {
		t.Fatalf("evidence = %d rows, want the template's 2", rows)
	}
}

func TestABrokenScriptLeavesNoTemplateBehind(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	err := buildTemplateString(p, t.Context(), template,
		`CREATE TABLE fine (x int); CREATE TABLE oops (x nosuchtype);`, sqlpolicy.ReadOnly())
	if err == nil {
		t.Fatal("a broken script built a template")
	}

	var exists bool
	if e := admin(t).QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, template).Scan(&exists); e != nil {
		t.Fatalf("looking for the template: %v", e)
	}
	if exists {
		t.Fatal("a failed build left a template behind for somebody to copy")
	}
}

// PostgreSQL's POSITION is relative to one statement; only the file line
// tells an organiser where to look.
func TestAScriptErrorNamesTheLineInTheFileTheStatementCameFrom(t *testing.T) {
	t.Parallel()

	refused := &gamedb.ScriptError{
		Line: 41982, SQLState: "42P01", Message: `relation "suspects" does not exist`, Position: 15,
	}
	if !strings.HasPrefix(refused.ScriptRejection(), "line 41982: ") {
		t.Fatalf("the rejection reads %q; the console's viewer looks for the line prefix",
			refused.ScriptRejection())
	}

	// Zero means unlocated: no line is claimed.
	unlocated := &gamedb.ScriptError{SQLState: "42P01", Message: `relation "suspects" does not exist`}
	if strings.HasPrefix(unlocated.ScriptRejection(), "line ") {
		t.Fatalf("a rejection with no line reads %q", unlocated.ScriptRejection())
	}
}

// PostgreSQL's verdict on the organiser's own SQL is theirs to read, and
// carries nothing about how this service reaches the cluster. Run against
// the real cluster (CLAUDE.md rule 10): the error's shape is pgx's.
func TestAScriptPostgreSQLRefusedComesBackAsTheAuthorsOwnToRead(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	err := buildTemplateString(p, t.Context(), template,
		`CREATE TABLE fine (x int); CREATE TABLE oops (x nosuchtype);`, sqlpolicy.ReadOnly())

	var refused *gamedb.ScriptError
	if !errors.As(err, &refused) {
		t.Fatalf("a script PostgreSQL refused came back as %T: %v", err, err)
	}
	if refused.SQLState != "42704" {
		t.Fatalf("SQLSTATE %q, want 42704 (undefined_object)", refused.SQLState)
	}
	if !strings.Contains(refused.ScriptRejection(), "nosuchtype") {
		t.Fatalf("the rejection reads %q; the type they misspelled is the whole point", refused.ScriptRejection())
	}
	if refused.Position <= 0 {
		t.Fatalf("the rejection carries position %d, want the offset PostgreSQL reported", refused.Position)
	}
	// Nothing of ours: no role, connection wrapper text or database name.
	for _, ours := range []string{gamedb.RoleAuthor, "failed to connect", "SASL", template} {
		if strings.Contains(refused.ScriptRejection(), ours) {
			t.Fatalf("the rejection names %q: %q", ours, refused.ScriptRejection())
		}
	}
}

// A failure in the middle of a batch is still blamed on its own statement
// and line.
func TestAFailureInsideABatchStillNamesItsOwnLine(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	var script strings.Builder
	script.WriteString("CREATE TABLE notes (n int);\n")
	// More than one batch, with the bad statement away from a boundary.
	for range 2500 {
		script.WriteString("INSERT INTO notes (n) VALUES (1);\n")
	}
	badLine := strings.Count(script.String(), "\n") + 1
	script.WriteString("INSERT INTO notes (n) VALUES ('not a number'::int);\n")
	for range 100 {
		script.WriteString("INSERT INTO notes (n) VALUES (2);\n")
	}

	err := buildTemplateString(p, t.Context(), template, script.String(), sqlpolicy.ReadOnly())

	var refused *gamedb.ScriptError
	if !errors.As(err, &refused) {
		t.Fatalf("a script PostgreSQL refused came back as %T: %v", err, err)
	}
	if refused.Line != badLine {
		t.Fatalf("the refusal names line %d, want %d — the batch lost track of which statement failed",
			refused.Line, badLine)
	}
}

// A batch is one implicit transaction, and VACUUM cannot run in one; the
// replay in runBatch must still run it.
func TestAStatementThatCannotRunInATransactionStillBuilds(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	script := "CREATE TABLE notes (n int);\n" +
		"INSERT INTO notes (n) VALUES (1), (2), (3);\n" +
		"VACUUM ANALYZE notes;\n"

	if err := buildTemplateString(p, t.Context(), template, script, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("a script with a VACUUM in it: %v", err)
	}

	conn := connectAsOwner(t, template)
	var rows int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM notes`).Scan(&rows); err != nil {
		t.Fatalf("reading the built template: %v", err)
	}
	if rows != 3 {
		t.Fatalf("the template holds %d rows, want 3", rows)
	}
}

// Buffered statements reach the server before the COPY block that needs
// their tables.
func TestABufferedBatchIsSentBeforeTheCopyBlockThatNeedsIt(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	script := "CREATE TABLE guests (id int, full_name text);\n" +
		"COPY guests (id, full_name) FROM stdin;\n" +
		"1\tIonescu\n2\tPopescu\n\\.\n" +
		"CREATE INDEX guests_name ON guests (full_name);\n"

	if err := buildTemplateString(p, t.Context(), template, script, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("a script whose COPY follows a CREATE TABLE: %v", err)
	}

	conn := connectAsOwner(t, template)
	var rows int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM guests`).Scan(&rows); err != nil {
		t.Fatalf("reading the built template: %v", err)
	}
	if rows != 2 {
		t.Fatalf("the template holds %d rows, want 2", rows)
	}
}

// pgx reports a refused login as a ConnectError wrapping a PgError, which
// names the role and the host. It must not become a ScriptError.
func TestAnAuthorLoginTheClusterRefusedIsNeverTheScriptsFault(t *testing.T) {
	requireCluster(t)

	user, password := gamedbtest.AdminCredentials(t)
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		"not-the-author-password")
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}

	template := named(t, "tpl")
	err = buildTemplateString(p, t.Context(), template, `CREATE TABLE fine (x int)`, sqlpolicy.ReadOnly())
	if err == nil {
		t.Fatal("a build ran the script over a connection that could not be made")
	}

	// The premise: a PgError really is inside.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("a refused login came back without PostgreSQL's own error inside it: %v", err)
	}
	var refused *gamedb.ScriptError
	if errors.As(err, &refused) {
		t.Fatalf("a refused login was reported as the script's fault: %q", refused.ScriptRejection())
	}
}

// The name is interpolated into DDL, which has no parameter binding.
func TestADatabaseNameThatIsNotAPlainIdentifierIsRefused(t *testing.T) {
	p := provisioner(t)

	for _, name := range []string{
		`x" WITH (FORCE); DROP DATABASE dbcontest_game; --`,
		"has space",
		"",
		strings.Repeat("x", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if err := buildTemplateString(p, t.Context(), name, `SELECT 1`, sqlpolicy.ReadOnly()); err == nil {
				t.Fatalf("built a template called %q", name)
			}
		})
	}
}

var _ = pgx.ErrNoRows

// TEMPORARY is a database-level privilege granted to PUBLIC by default,
// and a copy does not inherit it, so each instance must apply the policy.
func TestTemporaryTablesFollowThePolicyOnEveryInstance(t *testing.T) {
	t.Run("refused when the policy does not allow them", func(t *testing.T) {
		p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

		instance := named(t, "inst")
		if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
			t.Fatalf("creating the instance: %v", err)
		}

		writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)
		refused(t, writer, `CREATE TEMP TABLE scratch (x int)`)
	})

	t.Run("allowed when it does", func(t *testing.T) {
		policy := sqlpolicy.ReadWrite("evidence")
		policy.AllowTempTables = true
		p, template, policy := buildTemplate(t, policy)

		instance := named(t, "inst")
		if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
			t.Fatalf("creating the instance: %v", err)
		}

		writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)
		if _, err := writer.Exec(t.Context(), `CREATE TEMP TABLE scratch (x int)`); err != nil {
			t.Fatalf("temporary tables were refused although the policy allows them: %v", err)
		}
	})
}

// A backstop for the runner's semaphore: no third connection to one
// participant's database.
func TestAnInstanceRefusesMoreConnectionsThanAParticipantCanNeed(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	// Two: a running query plus its replacement being opened.
	first := connectAs(t, roleReader, testReaderPassword(t), instance)
	second := connectAs(t, roleReader, testReaderPassword(t), instance)
	if err := first.Ping(t.Context()); err != nil {
		t.Fatalf("the first connection is not usable: %v", err)
	}
	if err := second.Ping(t.Context()); err != nil {
		t.Fatalf("the second connection is not usable: %v", err)
	}

	if err := tryConnectAs(t, roleReader, testReaderPassword(t), instance); err == nil {
		t.Fatal("a third connection to one participant's database was allowed")
	}
}

func TestEitherCopyStrategyProducesAUsableDatabase(t *testing.T) {
	for _, strategy := range []gamedb.CopyStrategy{gamedb.StrategyWALLog, gamedb.StrategyFileCopy} {
		t.Run(string(strategy), func(t *testing.T) {
			p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())
			chosen, err := p.WithCopyStrategy(strategy)
			if err != nil {
				t.Fatalf("choosing %s: %v", strategy, err)
			}

			instance := named(t, "inst")
			if err := chosen.CreateInstance(t.Context(), template, instance, policy); err != nil {
				t.Fatalf("copying with %s: %v", strategy, err)
			}

			reader := connectAs(t, roleReader, testReaderPassword(t), instance)
			var suspects int
			if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspects); err != nil {
				t.Fatalf("the copy is not usable: %v", err)
			}
			if suspects != 2 {
				t.Fatalf("suspects = %d, want 2", suspects)
			}
		})
	}
}

func TestAnUnknownCopyStrategyIsRefusedUpFront(t *testing.T) {
	if _, err := provisioner(t).WithCopyStrategy("MAGIC"); err == nil {
		t.Fatal("an unknown copy strategy was accepted")
	}
}

// The reclaim sweep must never sever a connection: a plain DROP DATABASE
// refuses while one is open, and DropIdle reads that as "not now".
func TestDropIdleLeavesABusyDatabaseAloneAndDropsAnIdleOne(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	// Stands in for a query the Query Runner is still running.
	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	if _, err := reader.Exec(t.Context(), `SELECT 1`); err != nil {
		t.Fatalf("using the connection: %v", err)
	}

	dropped, err := p.DropIdle(t.Context(), instance)
	if err != nil {
		t.Fatalf("dropping a busy database returned an error: %v", err)
	}
	if dropped {
		t.Fatal("a database with a live connection was dropped")
	}
	if !instanceExists(t, instance) {
		t.Fatal("the busy instance was removed anyway")
	}

	if err := reader.Close(t.Context()); err != nil {
		t.Fatalf("closing the connection: %v", err)
	}

	dropped, err = p.DropIdle(t.Context(), instance)
	if err != nil {
		t.Fatalf("dropping an idle database: %v", err)
	}
	if !dropped {
		t.Fatal("an idle database was not dropped")
	}
	if instanceExists(t, instance) {
		t.Fatal("an idle instance was still there after DropIdle")
	}
}

// The reclaim sweep must be able to retry a database it already removed.
func TestDropIdleOnANameThatDoesNotExist(t *testing.T) {
	p := provisioner(t)

	dropped, err := p.DropIdle(t.Context(), named(t, "ghost"))
	if err != nil {
		t.Fatalf("dropping a database that was never created: %v", err)
	}
	if !dropped {
		t.Fatal("dropped = false for a name that never existed, want true (IF EXISTS)")
	}
}

func instanceExists(t *testing.T, name string) bool {
	t.Helper()
	var exists bool
	if err := admin(t).QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatalf("looking for %s: %v", name, err)
	}
	return exists
}

func TestDatabaseSizesMeasuresAWholeListAtOnce(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	first := named(t, "one")
	second := named(t, "two")
	for _, instance := range []string{first, second} {
		if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
			t.Fatalf("creating %s: %v", instance, err)
		}
	}

	sizes, err := p.DatabaseSizes(t.Context(), []string{first, second})
	if err != nil {
		t.Fatalf("DatabaseSizes: %v", err)
	}
	if len(sizes) != 2 {
		t.Fatalf("measured %d databases, want 2: %v", len(sizes), sizes)
	}
	for _, instance := range []string{first, second} {
		if sizes[instance] <= 0 {
			t.Fatalf("%s came back as %d bytes; a real database is never zero", instance, sizes[instance])
		}
	}
}

// pg_database_size raises an error for a missing name; a database dropped
// meanwhile must not cost the whole list its sizes.
func TestDatabaseSizesLeavesOutANameThatIsNotThere(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "real")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}
	ghost := named(t, "ghost")

	sizes, err := p.DatabaseSizes(t.Context(), []string{instance, ghost})
	if err != nil {
		t.Fatalf("a name that does not exist failed the whole batch: %v", err)
	}
	if _, measured := sizes[ghost]; measured {
		t.Fatalf("%s was measured although it does not exist", ghost)
	}
	if sizes[instance] <= 0 {
		t.Fatalf("%s came back as %d bytes", instance, sizes[instance])
	}
}

func TestDatabaseSizesOfNothingAsksNothing(t *testing.T) {
	p := provisioner(t)

	sizes, err := p.DatabaseSizes(t.Context(), nil)
	if err != nil {
		t.Fatalf("DatabaseSizes(nil): %v", err)
	}
	if len(sizes) != 0 {
		t.Fatalf("measured %d databases from an empty list", len(sizes))
	}
}

// A game script's author is any manager of one contest, while the cluster
// holds every contest's databases, so the script runs as game_author. Each
// case asserts SQLSTATE 42501 and PostgreSQL's phrase, not mere failure,
// which a typo would also produce.
func TestAHostileGameScriptIsRefusedTheThingsOnlyASuperuserCanDo(t *testing.T) {
	// Another olympiad's template. Its own suffix: the subtests share this
	// test's name, so a second "tpl" would be the database they build.
	sibling := named(t, "sib")
	if err := buildTemplateString(provisioner(t),
		t.Context(), sibling, detectiveScript, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building the other olympiad's template: %v", err)
	}

	for _, hostile := range []struct {
		name   string
		script string
		phrase string
	}{
		{
			name:   "a program run on the server",
			script: `CREATE TABLE loot (line text); COPY loot FROM PROGRAM 'id';`,
			phrase: "pg_execute_server_program",
		},
		{
			name:   "a program fed the server's data",
			script: `CREATE TABLE loot (line text); COPY loot TO PROGRAM 'cat > /tmp/loot';`,
			phrase: "pg_execute_server_program",
		},
		{
			name:   "a file read off the server",
			script: `CREATE TABLE loot (line text); COPY loot FROM '/etc/passwd';`,
			phrase: "pg_read_server_files",
		},
		{
			name:   "a file read by function",
			script: `CREATE TABLE loot AS SELECT pg_read_file('/etc/passwd');`,
			phrase: "permission denied for function pg_read_file",
		},
		{
			name:   "a participant role promoted to superuser",
			script: `ALTER ROLE ` + gamedb.RoleReader + ` SUPERUSER;`,
			phrase: "SUPERUSER attribute",
		},
		{
			name:   "a superuser of the author's own",
			script: `CREATE ROLE mine SUPERUSER LOGIN PASSWORD 'mine';`,
			phrase: "permission denied to create role",
		},
		{
			// Would undo the file-reading cases.
			name:   "the file-reading role granted to itself",
			script: `GRANT pg_read_server_files TO ` + gamedb.RoleAuthor + `;`,
			phrase: "permission denied to grant role",
		},
		{
			name:   "the cluster's password hashes",
			script: `CREATE TABLE loot AS SELECT * FROM pg_authid;`,
			phrase: "permission denied for table pg_authid",
		},
		{
			name:   "the list of every contest's databases",
			script: `CREATE TABLE loot AS SELECT * FROM pg_database;`,
			phrase: "permission denied for table pg_database",
		},
		{
			// dblink and postgres_fdw can open connections of their own.
			name:   "an extension that can open a connection",
			script: `CREATE EXTENSION dblink;`,
			phrase: `permission denied to create extension "dblink"`,
		},
		{
			// One statement: DROP DATABASE refuses inside the implicit transaction
			// of a multi-statement query, which would pass for the wrong reason.
			name:   "another olympiad's template dropped",
			script: `DROP DATABASE ` + sqlpolicy.QuoteIdentifier(sibling),
			phrase: "must be owner of database",
		},
	} {
		t.Run(hostile.name, func(t *testing.T) {
			p := provisioner(t)
			template := named(t, "tpl")

			err := buildTemplateString(p, t.Context(), template, hostile.script, sqlpolicy.ReadOnly())
			if err == nil {
				t.Fatalf("the cluster ran it: %s", hostile.script)
			}
			assertRefusedForPrivilege(t, err, hostile.phrase)

			if instanceExists(t, template) {
				t.Fatal("a refused script left a template behind")
			}
		})
	}

	// The error alone does not show the sibling survived.
	if !instanceExists(t, sibling) {
		t.Fatal("another contest's template was removed by a script in a different database")
	}
}

func assertRefusedForPrivilege(t *testing.T, err error, phrase string) {
	t.Helper()

	var refused *gamedb.ScriptError
	if !errors.As(err, &refused) {
		t.Fatalf("the build failed, but not with an error from the database: %v", err)
	}
	// 42501 is insufficient_privilege; a syntax error would prove nothing
	// about the role.
	if refused.SQLState != "42501" {
		t.Fatalf("refused with SQLSTATE %s (%s), want 42501 insufficient_privilege",
			refused.SQLState, refused.Message)
	}
	whole := refused.Message + " " + refused.Detail + " " + refused.Hint
	if !strings.Contains(whole, phrase) {
		t.Fatalf("refused with %q, which does not mention %q", strings.TrimSpace(whole), phrase)
	}
}

// An ordinary game still builds and can be read by a participant.
func TestAnOrdinaryGameStillBuildsUnderTheAuthorRole(t *testing.T) {
	const script = `
		-- A schema of the author's own: CREATE on the database, which is the
		-- other half of what the build is lent, and not the same privilege as
		-- CREATE on public.
		CREATE SCHEMA staging;
		CREATE TABLE staging.raw_swipes (line text);
		CREATE TABLE guests (id serial PRIMARY KEY, full_name text NOT NULL);
		CREATE TABLE keycard_events (
			id serial PRIMARY KEY,
			guest_id int NOT NULL REFERENCES guests (id),
			door text NOT NULL
		);
		CREATE INDEX keycard_events_door ON keycard_events (door);
		CREATE TYPE clearance AS ENUM ('none', 'staff');
		CREATE FUNCTION doors_used(int) RETURNS bigint LANGUAGE sql AS
			$$SELECT count(DISTINCT door) FROM keycard_events WHERE guest_id = $1$$;
		INSERT INTO guests (full_name) VALUES ('Margot Feilhaber'), ('Anton Rusu');
		INSERT INTO keycard_events (guest_id, door) VALUES (1, 'vault'), (1, 'lobby'), (2, 'lobby');
		CREATE VIEW busy_doors AS SELECT door, count(*) AS uses FROM keycard_events GROUP BY door;
	`

	p := provisioner(t)
	template := named(t, "tpl")
	policy := sqlpolicy.ReadOnly()
	if err := buildTemplateString(p, t.Context(), template, script, policy); err != nil {
		t.Fatalf("an ordinary game script no longer builds: %v", err)
	}

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	var doors int64
	if err := reader.QueryRow(t.Context(), `SELECT doors_used(1)`).Scan(&doors); err != nil {
		t.Fatalf("a participant cannot call the author's function: %v", err)
	}
	if doors != 2 {
		t.Fatalf("doors_used(1) = %d, want 2", doors)
	}
	var uses int64
	if err := reader.QueryRow(t.Context(),
		`SELECT uses FROM busy_doors WHERE door = 'lobby'`).Scan(&uses); err != nil {
		t.Fatalf("a participant cannot read the author's view: %v", err)
	}
	if uses != 2 {
		t.Fatalf("the lobby shows %d uses, want 2", uses)
	}

	var constraints int
	if err := reader.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_constraint WHERE contype = 'f'`).Scan(&constraints); err != nil {
		t.Fatalf("counting foreign keys: %v", err)
	}
	if constraints != 1 {
		t.Fatalf("the instance carries %d foreign keys, want 1", constraints)
	}
}

// What game_author was lent for the build is taken back before the
// template ships.
func TestAnInstanceLendsTheAuthorRoleNothing(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	author := connectAs(t, gamedb.RoleAuthor, gamedbtest.AuthorPassword(t), instance)
	refused(t, author, `CREATE TABLE public.planted (x int)`)
	refused(t, author, `CREATE SCHEMA planted`)
}

// Without a game_author credential the build is refused rather than run
// as the provisioning role. config.Load is the earlier gate.
func TestBuildingWithoutTheAuthorCredentialIsRefusedRatherThanRunAsTheProvisioner(t *testing.T) {
	requireCluster(t)

	user, password := gamedbtest.AdminCredentials(t)
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"), "")
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}

	template := named(t, "tpl")
	err = buildTemplateString(p, t.Context(), template, `CREATE TABLE fine (x int)`, sqlpolicy.ReadOnly())
	if !errors.Is(err, gamedb.ErrNoAuthorCredential) {
		t.Fatalf("BuildTemplate without a credential returned %v, want ErrNoAuthorCredential", err)
	}
	if instanceExists(t, template) {
		t.Fatal("a refused build created a database anyway")
	}
}

// Against the real cluster: a fake would only prove the SQL's spelling.
func TestClusterBytesCountsEveryDatabaseOnTheCluster(t *testing.T) {
	p := provisioner(t)

	before, err := p.ClusterBytes(t.Context())
	if err != nil {
		t.Fatalf("ClusterBytes: %v", err)
	}
	if before <= 0 {
		t.Fatalf("a cluster with databases on it measured %d bytes", before)
	}

	template := named(t, "tpl")
	if err := buildTemplateString(p, t.Context(), template,
		`CREATE TABLE bulk AS SELECT g, repeat('x', 400) AS pad FROM generate_series(1, 20000) g`,
		sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building a database to measure: %v", err)
	}

	after, err := p.ClusterBytes(t.Context())
	if err != nil {
		t.Fatalf("ClusterBytes: %v", err)
	}
	own, err := p.DatabaseSize(t.Context(), template)
	if err != nil {
		t.Fatalf("DatabaseSize: %v", err)
	}
	// The cluster is shared, so assert the direction and the floor.
	if after-before < own/2 {
		t.Fatalf("the total went from %d to %d after adding a database of %d bytes", before, after, own)
	}
}
