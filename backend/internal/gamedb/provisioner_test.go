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
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		gamedbtest.AuthorPassword(t))
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
	if err := p.BuildTemplate(t.Context(), template, detectiveScript, policy); err != nil {
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

// A read-only contest must produce a template whose privileges say so, whatever
// the validator does. This is the layer that has to hold when the validator
// does not.
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

// Writing is granted table by table, so the tables the policy does not name
// stay untouchable even in a contest that permits writing.
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

	writer := connectAs(t, gamedb.RoleWriter, gamedbtest.WriterPassword(t), instance)

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

	reader := connectAs(t, roleReader, testReaderPassword(t), instance)
	refused(t, reader, `SELECT count(*) FROM pg_database`)
	refused(t, reader, `SELECT count(*) FROM pg_stat_activity`)
}

// Rebuilding replaces the template, and a template with a connection cannot be
// copied — the discipline section 4.2 asks for. Both are the provisioner's job
// to keep, not the caller's to remember.
func TestATemplateCanBeRebuiltAndCopiedStraightAfter(t *testing.T) {
	p, template, policy := buildTemplate(t, sqlpolicy.ReadOnly())

	if err := p.BuildTemplate(t.Context(), template,
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

// Resetting is how a participant who has ruined their own data carries on. It
// has to work while they are still connected, which is what FORCE is for.
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
	if err := p.ResetInstance(t.Context(), template, instance, policy); err != nil {
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

// A script that does not run must fail the build rather than leave a template
// that is half a contest.
func TestABrokenScriptLeavesNoTemplateBehind(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	err := p.BuildTemplate(t.Context(), template,
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

// A failed build has two possible causes and one column to report them in, so
// the two have to be told apart where they happen. PostgreSQL's verdict on a
// statement the organiser wrote is theirs to read; it is also the only thing a
// failed build is allowed to say, because the alternatives all print how this
// service reaches the cluster.
//
// Run against the real cluster (CLAUDE.md rule 10): the shape of the error is
// pgx's, not ours, and a fixture of it would only prove we can spell it.
func TestAScriptPostgreSQLRefusedComesBackAsTheAuthorsOwnToRead(t *testing.T) {
	p := provisioner(t)
	template := named(t, "tpl")

	err := p.BuildTemplate(t.Context(), template,
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
	// The offset PostgreSQL located it at, which is what an editor underlines.
	if refused.Position <= 0 {
		t.Fatalf("the rejection carries position %d, want the offset PostgreSQL reported", refused.Position)
	}
	// And nothing of ours. `connect`/`host`/`port` would come from
	// Provisioner.connect's wrapper, the role name from pgx's own config dump.
	for _, ours := range []string{gamedb.RoleAuthor, "failed to connect", "SASL", template} {
		if strings.Contains(refused.ScriptRejection(), ours) {
			t.Fatalf("the rejection names %q: %q", ours, refused.ScriptRejection())
		}
	}
}

// The trap this separation exists for: pgx reports a refused login as a
// *pgconn.ConnectError that carries a *pgconn.PgError inside it, so anything
// deciding "was this the database's verdict?" by looking for a PgError says
// yes — and hands over the role, the addresses and the database name with it.
// internal/rpc.classify had to be rewritten around exactly this.
func TestAnAuthorLoginTheClusterRefusedIsNeverTheScriptsFault(t *testing.T) {
	requireCluster(t)

	user, password := gamedbtest.AdminCredentials(t)
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"),
		"not-the-author-password")
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}

	template := named(t, "tpl")
	err = p.BuildTemplate(t.Context(), template, `CREATE TABLE fine (x int)`, sqlpolicy.ReadOnly())
	if err == nil {
		t.Fatal("a build ran the script over a connection that could not be made")
	}

	// The premise: there really is a PgError in here, which is what makes a
	// type test at the far end the wrong instrument.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("a refused login came back without PostgreSQL's own error inside it: %v", err)
	}
	var refused *gamedb.ScriptError
	if errors.As(err, &refused) {
		t.Fatalf("a refused login was reported as the script's fault: %q", refused.ScriptRejection())
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
			if err := p.BuildTemplate(t.Context(), name, `SELECT 1`, sqlpolicy.ReadOnly()); err == nil {
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

// The organizer's database screen measures a whole contest's copies at once.
// One statement rather than one per database, because that page asks about
// every copy a contest owns and a round trip each would be hundreds of them.
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

// A name that is not on the cluster is left out of the answer rather than
// failing it. pg_database_size raises an error for one, so measuring by
// calling it per name would let a single database dropped between the
// core-database read and this call cost a whole screen its sizes — and a
// size is a decoration, not the thing the screen is for.
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

// An empty list costs no round trip: the organizer's list is filtered to the
// databases that still exist, and a contest whose copies are all reclaimed
// leaves nothing to ask about.
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

// A game script is staff-trusted, not platform-trusted.
//
// The route that accepts one is gated by a contest-scoped permission, so the
// author of a script is any manager of any single contest — while the cluster
// it runs on holds every other contest's template and every participant's
// database. Running it with the provisioning role's own privileges therefore
// made "manager of one draft contest" the same thing as "superuser on the
// game cluster". It runs as game_author instead, and what follows is the list
// of things that role cannot do.
//
// Each case asserts on the *refusal*, not merely on failure: SQLSTATE 42501
// (insufficient_privilege) plus the phrase PostgreSQL uses. A test that only
// checked "the build failed" would pass just as happily for a script with a
// typo in it, which proves nothing about who ran it.
func TestAHostileGameScriptIsRefusedTheThingsOnlyASuperuserCanDo(t *testing.T) {
	// A second template on the same cluster, standing in for another
	// olympiad's game. Its own suffix, not buildTemplate's "tpl": `named`
	// trims a long test name to fit PostgreSQL's 63 characters *before*
	// appending the suffix, and every subtest below shares this test's name,
	// so a second "tpl" here would be the very same database the subtests
	// build — and "cannot drop the currently open database" would look like
	// a refusal without being one.
	sibling := named(t, "sib")
	if err := provisioner(t).BuildTemplate(
		t.Context(), sibling, detectiveScript, sqlpolicy.ReadOnly()); err != nil {
		t.Fatalf("building the other olympiad's template: %v", err)
	}

	for _, hostile := range []struct {
		name   string
		script string
		phrase string
	}{
		{
			// A shell in the container.
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
			// The whole boundary in one statement: a participant role that is
			// a superuser is a cluster with no boundary at all.
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
			// Granting itself the role that would undo the first three cases.
			name:   "the file-reading role granted to itself",
			script: `GRANT pg_read_server_files TO ` + gamedb.RoleAuthor + `;`,
			phrase: "permission denied to grant role",
		},
		{
			// Reading the installation rather than the game.
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
			// Reaching out of this database. dblink and postgres_fdw are the
			// two ways SQL can open a connection of its own, and both are
			// untrusted extensions.
			name:   "an extension that can open a connection",
			script: `CREATE EXTENSION dblink;`,
			phrase: `permission denied to create extension "dblink"`,
		},
		{
			// One statement on purpose: a multi-statement simple query runs
			// in an implicit transaction, and DROP DATABASE refuses inside
			// one — which would make this pass without proving anything about
			// privileges.
			name:   "another olympiad's template dropped",
			script: `DROP DATABASE ` + gamedb.QuoteIdentifier(sibling),
			phrase: "must be owner of database",
		},
	} {
		t.Run(hostile.name, func(t *testing.T) {
			p := provisioner(t)
			template := named(t, "tpl")

			err := p.BuildTemplate(t.Context(), template, hostile.script, sqlpolicy.ReadOnly())
			if err == nil {
				t.Fatalf("the cluster ran it: %s", hostile.script)
			}
			assertRefusedForPrivilege(t, err, hostile.phrase)

			if instanceExists(t, template) {
				t.Fatal("a refused script left a template behind")
			}
		})
	}

	// The sibling is still there: the point of the last case is that it was
	// not dropped, which the error alone does not show.
	if !instanceExists(t, sibling) {
		t.Fatal("another contest's template was removed by a script in a different database")
	}
}

// assertRefusedForPrivilege insists the build failed because PostgreSQL said
// no, and said no for the stated reason.
func assertRefusedForPrivilege(t *testing.T, err error, phrase string) {
	t.Helper()

	// A *ScriptError and not a bare *pgconn.PgError: the hostile script is the
	// author's own SQL, so the refusal is one of the few a build may repeat
	// back to them, and this is where that classification is made.
	var refused *gamedb.ScriptError
	if !errors.As(err, &refused) {
		t.Fatalf("the build failed, but not with an error from the database: %v", err)
	}
	// 42501 is insufficient_privilege. A syntax error (42601) or an unknown
	// table (42P01) would mean the script was merely malformed, which says
	// nothing about the role that ran it.
	if refused.SQLState != "42501" {
		t.Fatalf("refused with SQLSTATE %s (%s), want 42501 insufficient_privilege",
			refused.SQLState, refused.Message)
	}
	whole := refused.Message + " " + refused.Detail + " " + refused.Hint
	if !strings.Contains(whole, phrase) {
		t.Fatalf("refused with %q, which does not mention %q", strings.TrimSpace(whole), phrase)
	}
}

// The other half of the same boundary: an ordinary game still builds, and a
// participant can still read it. A containment that broke real scripts would
// be a contest nobody can run.
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
	if err := p.BuildTemplate(t.Context(), template, script, policy); err != nil {
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

	// The foreign key came across, which is what the schema panel draws.
	var constraints int
	if err := reader.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_constraint WHERE contype = 'f'`).Scan(&constraints); err != nil {
		t.Fatalf("counting foreign keys: %v", err)
	}
	if constraints != 1 {
		t.Fatalf("the instance carries %d foreign keys, want 1", constraints)
	}
}

// What the author role was lent for the build is taken back before the
// template ships, so a participant's copy does not carry a role that may
// create objects in the game's own schema.
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

// A provisioner with no game_author credential refuses to build rather than
// falling back to the provisioning role.
//
// The distinction is the whole fix: an unset GAME_AUTHOR_PASSWORD must not be
// a way to have every game script run as a superuser again. A deployment is
// stopped earlier still, when config.Load reads the environment, so this is
// the last of two gates rather than the only one.
func TestBuildingWithoutTheAuthorCredentialIsRefusedRatherThanRunAsTheProvisioner(t *testing.T) {
	requireCluster(t)

	user, password := gamedbtest.AdminCredentials(t)
	p, err := gamedb.NewProvisioner(admin(t), gamedbtest.DSN(t, user, password, "postgres"), "")
	if err != nil {
		t.Fatalf("building the provisioner: %v", err)
	}

	template := named(t, "tpl")
	err = p.BuildTemplate(t.Context(), template, `CREATE TABLE fine (x int)`, sqlpolicy.ReadOnly())
	if !errors.Is(err, gamedb.ErrNoAuthorCredential) {
		t.Fatalf("BuildTemplate without a credential returned %v, want ErrNoAuthorCredential", err)
	}
	if instanceExists(t, template) {
		t.Fatal("a refused build created a database anyway")
	}
}

// The measurement the pool's byte budget is decided on. Against the real
// cluster because that is the only place the number means anything: a fake
// would only prove the SQL was spelled the way the fake expects.
func TestClusterBytesCountsEveryDatabaseOnTheCluster(t *testing.T) {
	p := provisioner(t)

	before, err := p.ClusterBytes(t.Context())
	if err != nil {
		t.Fatalf("ClusterBytes: %v", err)
	}
	if before <= 0 {
		t.Fatalf("a cluster with databases on it measured %d bytes", before)
	}

	// A database this test makes has to move the number, or the total is not
	// a total.
	template := named(t, "tpl")
	if err := p.BuildTemplate(t.Context(), template,
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
	// The cluster is shared with whatever else is running, so the assertion
	// is the direction and the floor rather than an equality: the new
	// database is in the total.
	if after-before < own/2 {
		t.Fatalf("the total went from %d to %d after adding a database of %d bytes", before, after, own)
	}
}
