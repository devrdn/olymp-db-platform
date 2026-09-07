package gamedb_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamedb/gamedbtest"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
)

// The detective's own database: a small schema and some data, the way an
// author would upload it.
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
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"))
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}
	return p
}

// named returns a database name unique to this test, dropped afterwards.
func named(t *testing.T, suffix string) string {
	t.Helper()

	// PostgreSQL stops at 63 characters, and a Go test name is easily longer.
	// The suffix is appended *after* trimming, or two names in one test trim to
	// the same thing — which is how a template and an instance once ended up
	// as one database, the instance's creation dropping the template first.
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
	if err := p.BuildTemplate(t.Context(), gamedb.TemplateSpec{
		Name: template, Script: detectiveScript, Policy: policy,
	}); err != nil {
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

	reader := connectAs(t, roleReader, testReaderPassword, instance)
	var suspects int
	if err := reader.QueryRow(t.Context(), `SELECT count(*) FROM suspects`).Scan(&suspects); err != nil {
		t.Fatalf("a participant cannot read the game: %v", err)
	}
	if suspects != 2 {
		t.Fatalf("suspects = %d, want 2", suspects)
	}
}

// The whole reason a template exists: copying it is what a participant gets,
// and the copy must be theirs alone.
func TestTwoInstancesDoNotShareData(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	first, second := named(t, "one"), named(t, "two")
	for _, name := range []string{first, second} {
		if err := p.CreateInstance(t.Context(), template, name, policy); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, first)
	if _, err := writer.Exec(t.Context(), `INSERT INTO evidence (note) VALUES ('planted')`); err != nil {
		t.Fatalf("the writer cannot write to a writable table: %v", err)
	}

	other := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, second)
	var rows int
	if err := other.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("reading the second instance: %v", err)
	}
	if rows != 2 {
		t.Fatalf("the second instance sees %d rows; one participant's write reached another", rows)
	}
}

// A read-only contest must produce a template whose privileges say so, whatever
// the validator does. This is the layer that has to hold when the validator
// does not.
func TestAReadOnlyTemplateGrantsNothingThatWrites(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword, instance)
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

// Writing is granted table by table, so the tables the policy does not name
// stay untouchable even in a contest that permits writing.
func TestWritingReachesOnlyTheTablesThePolicyNames(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)

	if _, err := writer.Exec(t.Context(), `INSERT INTO evidence (note) VALUES ('a note')`); err != nil {
		t.Fatalf("the named table is not writable: %v", err)
	}
	// suspects is not on the list.
	refused(t, writer, `INSERT INTO suspects (name) VALUES ('someone')`)
	refused(t, writer, `UPDATE suspects SET city = 'nowhere'`)
	refused(t, writer, `DROP TABLE evidence`)
}

// Their own objects go in `work`, and only there — the game's own schema stays
// structurally untouchable.
func TestOwnObjectsLiveInWorkAndNowhereElse(t *testing.T) {
	policy := sqlpolicy.ReadWrite()
	policy.AllowOwnTables = true
	policy.AllowCreateView = true
	p, template, policy := buildTemplate(t, policy)

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)

	if _, err := writer.Exec(t.Context(), `CREATE TABLE work.notes (x int)`); err != nil {
		t.Fatalf("their own schema is not writable: %v", err)
	}
	if _, err := writer.Exec(t.Context(), `CREATE VIEW work.clues AS SELECT * FROM evidence`); err != nil {
		t.Fatalf("a view in their own schema was refused: %v", err)
	}
	refused(t, writer, `CREATE TABLE public.mine (x int)`)
}

// The hardening travels with the template, so an instance is not a place where
// the catalogue rules quietly lapse.
func TestAnInstanceInheritsTheCatalogueRules(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword, instance)
	refused(t, reader, `SELECT count(*) FROM pg_database`)
	refused(t, reader, `SELECT count(*) FROM pg_stat_activity`)
}

// Rebuilding replaces the template, and a template with a connection cannot be
// copied — the discipline section 4.2 asks for. Both are the provisioner's job
// to keep, not the caller's to remember.
func TestATemplateCanBeRebuiltAndCopiedStraightAfter(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	if err := p.BuildTemplate(t.Context(), gamedb.TemplateSpec{
		Name:   template,
		Script: `CREATE TABLE suspects (id int); INSERT INTO suspects VALUES (7);`,
		Policy: sqlpolicy.ReadOnly(),
	}); err != nil {
		t.Fatalf("rebuilding: %v", err)
	}

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("copying straight after a rebuild: %v", err)
	}

	reader := connectAs(t, roleReader, testReaderPassword, instance)
	var only int
	if err := reader.QueryRow(t.Context(), `SELECT id FROM suspects`).Scan(&only); err != nil {
		t.Fatalf("the rebuilt data is not there: %v", err)
	}
	if only != 7 {
		t.Fatalf("id = %d; the instance came from the old template", only)
	}
}

// Resetting is how a participant who has ruined their own data carries on. It
// has to work while they are still connected, which is what FORCE is for.
func TestResettingReplacesTheInstanceUnderALiveConnection(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)
	if _, err := writer.Exec(t.Context(), `DELETE FROM evidence`); err != nil {
		t.Fatalf("emptying the table: %v", err)
	}

	// Their connection is still open, on purpose.
	if err := p.ResetInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("resetting: %v", err)
	}

	fresh := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)
	var rows int
	if err := fresh.QueryRow(t.Context(), `SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("reading the reset instance: %v", err)
	}
	if rows != 2 {
		t.Fatalf("evidence = %d rows, want the template's 2", rows)
	}
}

// A script that does not run must fail the build rather than leave a template
// that is half a contest.
func TestABrokenScriptLeavesNoTemplateBehind(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	err := p.BuildTemplate(t.Context(), gamedb.TemplateSpec{
		Name:   template,
		Script: `CREATE TABLE fine (x int); CREATE TABLE oops (x nosuchtype);`,
		Policy: sqlpolicy.ReadOnly(),
	})
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

// The name is interpolated into DDL, where SQL has no parameter binding. The
// policy already refuses a table name that is not a plain identifier; a
// database name has to be refused the same way and for the same reason.
func TestADatabaseNameThatIsNotAPlainIdentifierIsRefused(t *testing.T) {
	p := provisioner(t)

	for _, name := range []string{
		`x" WITH (FORCE); DROP DATABASE dbcontest_game; --`,
		"has space",
		"",
		strings.Repeat("x", 64),
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.BuildTemplate(t.Context(), gamedb.TemplateSpec{
				Name: name, Script: `SELECT 1`, Policy: sqlpolicy.ReadOnly(),
			}); err == nil {
				t.Fatalf("built a template called %q", name)
			}
		})
	}
}

var _ = pgx.ErrNoRows

// Temporary tables are the one permission that cannot travel with the
// template. PostgreSQL grants TEMPORARY on a database to PUBLIC by default,
// and a database-level privilege lives on the pg_database row, which a copy
// does not inherit — so a policy that only reached the template would leave
// `allow_temp_tables: false` quietly meaning nothing.
func TestTemporaryTablesFollowThePolicyOnEveryInstance(t *testing.T) {
	t.Run("refused when the policy does not allow them", func(t *testing.T) {
		p, template, policy := buildTemplate(t, sqlpolicy.ReadWrite("evidence"))

		instance := named(t, "inst")
		if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
			t.Fatalf("creating the instance: %v", err)
		}

		writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)
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

		writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword, instance)
		if _, err := writer.Exec(t.Context(), `CREATE TEMP TABLE scratch (x int)`); err != nil {
			t.Fatalf("temporary tables were refused although the policy allows them: %v", err)
		}
	})
}

// Nothing should ever open a third connection to one participant's database.
// The runner's semaphore is what enforces that; this is what holds if the
// runner is wrong, which is the only reason to have it.
func TestAnInstanceRefusesMoreConnectionsThanAParticipantCanNeed(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	// Two is the allowance: a query running while its replacement is opened.
	first := connectAs(t, roleReader, testReaderPassword, instance)
	second := connectAs(t, roleReader, testReaderPassword, instance)
	if err := first.Ping(t.Context()); err != nil {
		t.Fatalf("the first connection is not usable: %v", err)
	}
	if err := second.Ping(t.Context()); err != nil {
		t.Fatalf("the second connection is not usable: %v", err)
	}

	if err := tryConnectAs(t, roleReader, testReaderPassword, instance); err == nil {
		t.Fatal("a third connection to one participant's database was allowed")
	}
}

// Both strategies must actually produce a usable copy: the choice is a
// measurement to be made on a real template, and a setting that only works one
// way is not a choice.
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

			reader := connectAs(t, roleReader, testReaderPassword, instance)
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

// An unknown strategy is refused when it is set, not at the first copy — which
// would be during a contest.
func TestAnUnknownCopyStrategyIsRefusedUpFront(t *testing.T) {
	if _, err := provisioner(t).WithCopyStrategy("MAGIC"); err == nil {
		t.Fatal("an unknown copy strategy was accepted")
	}
}

// The reclaim sweep (internal/provisioning.Service.Reclaim) must never sever
// a connection to decide whether a database is safe to remove — only
// PostgreSQL's own refusal proves that, and this is what has to provoke it
// for real: a plain DROP DATABASE against a database with an open connection
// really does refuse, and DropIdle's job is to read that refusal as "leave it
// for next time" rather than as an error worth reporting.
func TestDropIdleLeavesABusyDatabaseAloneAndDropsAnIdleOne(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	instance := named(t, "inst")
	if err := p.CreateInstance(t.Context(), template, instance, policy); err != nil {
		t.Fatalf("creating the instance: %v", err)
	}

	// A live connection, standing in for a query the Query Runner is still
	// executing against this instance.
	reader := connectAs(t, roleReader, testReaderPassword, instance)
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

// A name that has already been dropped, or never existed, is not an error —
// the reclaim sweep must be able to retry a database it already removed
// without that counting as a failure.
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
