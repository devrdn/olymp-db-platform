package provisioning_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

func TestSettingTheScriptStoresItPendingAndBuildsNothingYet(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	contest := uuid.New()

	saved, err := service.SetScript(t.Context(), uuid.New(), contest, `CREATE TABLE guests (id int);`)
	if err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	if saved.Status != provisioning.TemplatePending {
		t.Fatalf("stored as %q, want pending", saved.Status)
	}
	if saved.Version != 1 {
		t.Fatalf("first script is version %d, want 1", saved.Version)
	}
	// The name is derived, never supplied: a caller that could name the
	// database could name somebody else's.
	if !strings.HasPrefix(saved.Database, "game_tpl_c") {
		t.Fatalf("database is %q", saved.Database)
	}
	// Building creates a database and runs an author's whole script inside
	// it. Doing that inside the request that saved the script is what the
	// pending status exists to avoid.
	if len(cluster.names) != 0 {
		t.Fatal("built the game inside the request that stored the script")
	}
	if store.template.Script == "" {
		t.Fatal("the script was not stored")
	}
}

// Replacing a game bumps its version, every copy made from the old one is
// then stale, and a stale copy is dropped and made again. In a running
// olympiad that is every participant losing their database at once.
func TestTheGameOfARunningContestCannotBeReplaced(t *testing.T) {
	t.Parallel()
	service, store, _ := games(false)

	_, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `SELECT 1`)
	if !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("answered %v, want ErrGameNotEditable", err)
	}
	if store.present {
		t.Fatal("stored the script anyway")
	}
}

func TestAnEmptyOrOversizedScriptIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		script string
		want   error
	}{
		{"empty", "", provisioning.ErrScriptEmpty},
		{"too long", strings.Repeat("-", provisioning.MaxScriptBytes+1), provisioning.ErrScriptTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, store, _ := games(true)
			if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), tc.script); !errors.Is(err, tc.want) {
				t.Fatalf("answered %v, want %v", err, tc.want)
			}
			if store.present {
				t.Fatal("stored a script that was refused")
			}
		})
	}
}

func TestASecondScriptBumpsTheVersionSoEveryCopyBecomesStale(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetScript(t.Context(), uuid.New(), contest, `SELECT 1`); err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := service.SetScript(t.Context(), uuid.New(), contest, `SELECT 2`)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second script is version %d, want 2", second.Version)
	}
}

// aDefinition is a small, valid game — the shape a detective game actually
// needs (definition_test.go's own doc gives the same example): a suspects
// table with a primary key, nothing more.
func aDefinition() provisioning.Definition {
	return provisioning.Definition{Tables: []provisioning.TableDefinition{
		{
			Name: "suspects",
			Columns: []provisioning.ColumnDefinition{
				{Name: "id", Type: provisioning.ColumnInteger},
				{Name: "name", Type: provisioning.ColumnText},
			},
			PrimaryKey: []string{"id"},
		},
	}}
}

func TestSettingTheDefinitionStoresItPendingAndBuildsNothingYet(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	contest := uuid.New()

	saved, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition())
	if err != nil {
		t.Fatalf("setting the definition: %v", err)
	}
	if saved.Status != provisioning.TemplatePending {
		t.Fatalf("stored as %q, want pending", saved.Status)
	}
	if saved.Source != provisioning.SourceBuilder {
		t.Fatalf("stored as source %q, want builder", saved.Source)
	}
	// Building creates a database and runs a build inside it. Doing that
	// inside the request that saved the definition is what the pending
	// status exists to avoid, the same reasoning SetScript's own test gives.
	if len(cluster.names) != 0 {
		t.Fatal("built the game inside the request that stored the definition")
	}
	if len(store.template.Definition.Tables) == 0 {
		t.Fatal("the definition was not stored")
	}
}

// A contest whose game may no longer be replaced must refuse the table
// builder's own way in exactly as it refuses the editor's — replacing a
// game bumps its version and makes every participant's copy stale, whatever
// produced the replacement.
func TestTheGameOfARunningContestCannotHaveItsDefinitionReplaced(t *testing.T) {
	t.Parallel()
	service, store, _ := games(false)

	_, err := service.SetDefinition(t.Context(), uuid.New(), uuid.New(), aDefinition())
	if !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("answered %v, want ErrGameNotEditable", err)
	}
	if store.present {
		t.Fatal("stored the definition anyway")
	}
}

// SetDefinition's own validation runs before anything is asked of storage —
// the same ordering TestAnEmptyOrOversizedScriptIsRefusedBeforeAnythingIsAsked
// proves for SetScript. Definition.Validate's own tests (definition_test.go)
// cover every refusal in depth; this is only the wiring between the two.
func TestAnInvalidDefinitionIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)

	_, err := service.SetDefinition(t.Context(), uuid.New(), uuid.New(), provisioning.Definition{})
	if !errors.Is(err, provisioning.ErrDefinitionEmpty) {
		t.Fatalf("answered %v, want ErrDefinitionEmpty", err)
	}
	if store.present {
		t.Fatal("stored a definition that was refused")
	}
}

func TestASecondDefinitionBumpsTheVersionSoEveryCopyBecomesStale(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("first: %v", err)
	}
	second := aDefinition()
	second.Tables[0].Name = "witnesses"
	saved, err := service.SetDefinition(t.Context(), uuid.New(), contest, second)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if saved.Version != 2 {
		t.Fatalf("second definition is version %d, want 2", saved.Version)
	}
}

// The table builder's own way of writing a game gets its own action code
// (audit.ActionGameDefinitionSet), and nothing asserted it: a save that
// stopped recording, or one folded back into contest.game_script_set, would
// have left the trail unable to say which of the three ways built a game —
// and the journal is append-only, so a missing entry cannot be filled in
// afterwards.
func TestSavingADefinitionIsRecordedUnderItsOwnAction(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	contest, actor := uuid.New(), uuid.New()

	saved, err := service.SetDefinition(t.Context(), actor, contest, aDefinition())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if len(trail.entries) != 1 {
		t.Fatalf("the trail holds %d entr(ies), want 1: %+v", len(trail.entries), trail.entries)
	}
	entry := trail.entries[0]
	if entry.Action != audit.ActionGameDefinitionSet {
		t.Fatalf("action = %q, want %q", entry.Action, audit.ActionGameDefinitionSet)
	}
	if entry.Entity != "contest" || entry.EntityID != contest.String() {
		t.Fatalf("entry points at %s/%s, want contest/%s", entry.Entity, entry.EntityID, contest)
	}
	if entry.ActorID == nil || *entry.ActorID != actor {
		t.Fatalf("entry names actor %v, want %s", entry.ActorID, actor)
	}
	if entry.Payload["version"] != saved.Version || entry.Payload["tables"] != len(aDefinition().Tables) {
		t.Fatalf("payload = %+v, want version %d and %d table(s)",
			entry.Payload, saved.Version, len(aDefinition().Tables))
	}
	// The definition itself is not in the payload: the trail is a list of who
	// did what, not a second copy of the row's own content (SetDefinition's
	// own doc), and the version is what identifies which definition this was.
	for _, value := range entry.Payload {
		if _, isDefinition := value.(provisioning.Definition); isDefinition {
			t.Fatalf("the payload carries the whole definition: %+v", entry.Payload)
		}
	}
}

// A table's own data is a CSV file whose first line names the table's
// columns, in order (tabledata.go's own header check) — SetDefinition used
// to replace the whole description with no check against that file at all,
// so a rename, a type change or a column's removal saved cleanly and left
// the file silently disagreeing with the new definition. This is the
// boundary ErrDefinitionTableLocked now enforces: any of the three, once a
// table holds a single row, is refused before anything is written — the
// same freeze the table builder's own screen already enforces client-side
// (game-builder.tsx's own doc, "Why a table with data locks its own
// structure").
func TestADefinitionChangeThatWouldOrphanATablesOwnDataIsRefused(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the first definition: %v", err)
	}
	// "suspects" now holds three rows, the way CompleteTableUpload leaves it
	// — this test needs only the row saying so, never a real file on disk,
	// to prove the refusal at the SetDefinition boundary.
	dataID := uuid.New()
	store.tableData = map[uuid.UUID]provisioning.TableData{
		dataID: {ID: dataID, ContestID: contest, Table: "suspects", Status: provisioning.TableDataComplete, Lines: 3},
	}

	for _, tc := range []struct {
		name string
		next provisioning.Definition
	}{
		{
			"column renamed",
			provisioning.Definition{Tables: []provisioning.TableDefinition{{
				Name: "suspects",
				Columns: []provisioning.ColumnDefinition{
					{Name: "id", Type: provisioning.ColumnInteger},
					{Name: "full_name", Type: provisioning.ColumnText},
				},
				PrimaryKey: []string{"id"},
			}}},
		},
		{
			"column type changed",
			provisioning.Definition{Tables: []provisioning.TableDefinition{{
				Name: "suspects",
				Columns: []provisioning.ColumnDefinition{
					{Name: "id", Type: provisioning.ColumnInteger},
					{Name: "name", Type: provisioning.ColumnInteger},
				},
				PrimaryKey: []string{"id"},
			}}},
		},
		{
			"column removed",
			provisioning.Definition{Tables: []provisioning.TableDefinition{{
				Name:       "suspects",
				Columns:    []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
				PrimaryKey: []string{"id"},
			}}},
		},
		{
			"table removed outright",
			provisioning.Definition{Tables: []provisioning.TableDefinition{{
				Name:    "witnesses",
				Columns: []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
			}}},
		},
		{
			"primary key changed",
			provisioning.Definition{Tables: []provisioning.TableDefinition{{
				Name: "suspects",
				Columns: []provisioning.ColumnDefinition{
					{Name: "id", Type: provisioning.ColumnInteger},
					{Name: "name", Type: provisioning.ColumnText},
				},
				// No primary key at all now, where there was one.
			}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.SetDefinition(t.Context(), uuid.New(), contest, tc.next)
			if !errors.Is(err, provisioning.ErrDefinitionTableLocked) {
				t.Fatalf("answered %v, want ErrDefinitionTableLocked", err)
			}
			// The version must not have moved: a refused save changed
			// nothing about the game that was already there.
			if store.template.Version != 1 {
				t.Fatalf("version is %d after a refused save, want 1", store.template.Version)
			}
		})
	}
}

// The freeze is per table, not per definition: a table that already holds
// data locks its own structure, but an organiser may still add an entirely
// new table beside it, or resave the locked table completely unchanged.
func TestATableWithDataMayStillGainANewSiblingTable(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the first definition: %v", err)
	}
	dataID := uuid.New()
	store.tableData = map[uuid.UUID]provisioning.TableData{
		dataID: {ID: dataID, ContestID: contest, Table: "suspects", Status: provisioning.TableDataComplete, Lines: 3},
	}

	next := aDefinition()
	next.Tables = append(next.Tables, provisioning.TableDefinition{
		Name:    "witnesses",
		Columns: []provisioning.ColumnDefinition{{Name: "id", Type: provisioning.ColumnInteger}},
	})

	saved, err := service.SetDefinition(t.Context(), uuid.New(), contest, next)
	if err != nil {
		t.Fatalf("adding a sibling table beside a locked one: %v", err)
	}
	if saved.Version != 2 {
		t.Fatalf("version is %d, want 2", saved.Version)
	}
}

// A completed file with no data rows — a header with nothing under it — is
// not locked: loadTableData's own copy reader always skips the header line
// unconditionally (tableDataCopyReader.advance), so nothing a build would
// ever read disagrees with a new structure while the table is still empty.
func TestATableWithACompletedButEmptyFileIsNotLocked(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the first definition: %v", err)
	}
	dataID := uuid.New()
	store.tableData = map[uuid.UUID]provisioning.TableData{
		dataID: {ID: dataID, ContestID: contest, Table: "suspects", Status: provisioning.TableDataComplete, Lines: 0},
	}

	renamed := provisioning.Definition{Tables: []provisioning.TableDefinition{{
		Name: "suspects",
		Columns: []provisioning.ColumnDefinition{
			{Name: "id", Type: provisioning.ColumnInteger},
			{Name: "full_name", Type: provisioning.ColumnText},
		},
		PrimaryKey: []string{"id"},
	}}}
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, renamed); err != nil {
		t.Fatalf("renaming a column of an empty table: %v", err)
	}
}

func TestBuildingRunsTheScriptAndRecordsTheOutcome(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	contest := uuid.New()
	if _, err := service.SetScript(t.Context(), uuid.New(), contest, `CREATE TABLE guests (id int);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("finished as %q, want ready", built.Status)
	}
	if len(cluster.scripts) != 1 || cluster.scripts[0] != `CREATE TABLE guests (id int);` {
		t.Fatalf("built with %v", cluster.scripts)
	}
	if len(store.finished) != 1 || store.finished[0].err != "" {
		t.Fatalf("recorded %+v", store.finished)
	}
}

// scriptRefusal stands for gamedb.ScriptError: PostgreSQL's verdict on a
// statement the organiser wrote, which is the one build failure whose words
// are theirs to read. A bare errors.New here would be a test of the *other*
// branch wearing this one's name — the mistake internal/rpc's own table of
// failures records under "a database error".
type scriptRefusal struct{ says string }

func (s scriptRefusal) Error() string           { return s.says }
func (s scriptRefusal) ScriptRejection() string { return s.says }

// connectFailure is the shape the game cluster really produces when a build
// cannot reach it: gamedb.Provisioner.connect's own wrapper around pgx's
// *pgconn.ConnectError, which prints the role it authenticated as, every
// address it dialled and the database it asked for. Written out as a literal
// rather than constructed, because what this file has to pin is the text —
// these are the substrings that must not survive into a response body or an
// audit payload.
const connectFailureText = "connect to game_tpl_cabc123 as game_author: " +
	"failed to connect to `user=game_author database=game_tpl_cabc123`: " +
	`[::1]:5433 (pg-game): failed SASL auth: FATAL: password authentication ` +
	`failed for user "game_author" (SQLSTATE 28P01)`

// leaked names the pieces of connectFailureText that describe this
// installation rather than anybody's SQL.
var leaked = []string{"game_author", "5433", "pg-game", "28P01", "SASL"}

// The organiser is the person who has to fix the script, and "the build
// failed" tells them nothing they can act on.
func TestAFailedBuildKeepsThePostgresErrorForWhoeverWroteTheScript(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	cluster.fail = scriptRefusal{says: `the game script was refused: type "nosuchtype" does not exist (SQLSTATE 42704)`}
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `CREATE TABLE oops (x nosuchtype);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}
	if !strings.Contains(store.finished[0].err, "nosuchtype") {
		t.Fatalf("recorded %q — PostgreSQL's own words are the useful ones", store.finished[0].err)
	}
	if built.BuildError != store.finished[0].err {
		t.Fatalf("served %q and recorded %q; the organiser reads both", built.BuildError, store.finished[0].err)
	}
}

// The other half of the same rule, and the one the review found open: a build
// that failed for a reason of ours must not describe our cluster to a contest
// manager, nor leave that description in a trail nobody can edit.
//
// Asserted on the two sinks and not on a log line: GameHandler.status serves
// Template.BuildError verbatim behind contest.view, and the contest.game_built
// payload is append-only.
func TestABuildThatFailedForOurOwnReasonsDescribesNoneOfOurInfrastructure(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	cluster.fail = errors.New(connectFailureText)
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `CREATE TABLE fine (x int);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err == nil {
		t.Fatal("a build the cluster refused was reported as a clean tick; the log is the only place the cause survives now")
	}
	if !strings.Contains(err.Error(), "pg-game") {
		t.Fatalf("the cause returned for the log was %v; it has to keep what the organiser no longer gets", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}

	// What the row says — which is what GET /contests/{id}/game serves.
	if store.finished[0].err != provisioning.BuildFailedInternally {
		t.Fatalf("recorded %q, want the fixed sentence", store.finished[0].err)
	}
	if built.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("served %q, want the fixed sentence", built.BuildError)
	}

	// What the trail keeps. Saving the script recorded an entry of its own, so
	// the build's is picked out by its action rather than by its position.
	var built0 *audit.Entry
	for i, entry := range trail.entries {
		if entry.Action == audit.ActionGameBuilt {
			built0 = &trail.entries[i]
		}
	}
	if built0 == nil {
		t.Fatalf("recorded %+v, with no contest.game_built entry among them", trail.entries)
	}
	recorded, _ := built0.Payload["error"].(string)
	if recorded != provisioning.BuildFailedInternally {
		t.Fatalf("the audit payload says %q, want the fixed sentence", recorded)
	}

	for _, secret := range leaked {
		for label, text := range map[string]string{
			"the stored build error": store.finished[0].err,
			"the served build error": built.BuildError,
			"the audit payload":      recorded,
		} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s names %q: %q", label, secret, text)
			}
		}
	}
}

// A script refusal is the one build failure whose text is the author's own,
// and it is also the only one whose *size* and *bytes* the author chooses:
// the reader quotes their file back at them, and their file is untrusted
// input up to GAME_UPLOAD_MAX_FILE_BYTES.
//
// Two bounds, one place. The length, because build_error is an unbounded
// `text` column and the same string is copied into the append-only
// contest.game_built payload as well (CLAUDE.md rule 2: the bound belongs in
// the domain, at the field, not only in the request that carried it). And
// storability, because a `text` column is UTF-8 *and* NUL-free while a dump
// is raw bytes: PostgreSQL refuses either with SQLSTATE 22021, and that
// refusal comes from FinishBuild — so the row is never moved out of
// 'building', staleBuildAfter claims it again, and the game spends every
// sweep doing a DROP DATABASE and a CREATE DATABASE on the cluster an
// olympiad is running on, for ever, without ever becoming ready.
//
// The assertion is storability and not `utf8.ValidString`, which is the shape
// this test had while the defect was open: \x00 is *valid* UTF-8 in Go and
// forbidden in a `text` column, so the encoding check was green on the exact
// byte that wedges the build. A `pg_dump -Fc` uploaded by mistake — the
// commonest export error there is — is full of them.
func TestAScriptRefusalIsBoundedAndFitToStoreBeforeItIsStored(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	// What the reader hands back for a line of a dump it refused: the file's
	// own bytes, in the file's own quantity.
	cluster.fail = scriptRefusal{says: "line 1: \xff\xfe\x00 " + strings.Repeat("q", 4<<20)}

	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `CREATE TABLE fine (x int);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	recordedEntry := "" // the audit payload's copy of the same string
	for _, entry := range trail.entries {
		if entry.Action == audit.ActionGameBuilt {
			recordedEntry, _ = entry.Payload["error"].(string)
		}
	}

	for label, text := range map[string]string{
		"the stored build error": store.finished[0].err,
		"the served build error": built.BuildError,
		"the audit payload":      recordedEntry,
	} {
		if len(text) > provisioning.MaxBuildErrorBytes {
			t.Fatalf("%s is %d bytes, past the %d the domain allows",
				label, len(text), provisioning.MaxBuildErrorBytes)
		}
		if !utf8.ValidString(text) {
			t.Fatalf("%s is not valid UTF-8, so the column it goes to refuses it: %q", label, text)
		}
		if strings.ContainsRune(text, 0) {
			t.Fatalf("%s still carries a NUL byte, which `text` and `jsonb` both refuse "+
				"with 22021 — the build can never be finished: %q", label, text)
		}
		// Bounded, but still the author's own verdict: the useful part is the
		// front of it.
		if !strings.HasPrefix(text, "line 1: ") {
			t.Fatalf("%s = %q, and no longer starts with what the reader said", label, text)
		}
	}
}

// The same rule for the *core* database's own failure, which reached the same
// column by a different line: the policy read happens before the cluster is
// touched at all, and its error text is a connection string of ours.
func TestAPolicyThatCouldNotBeReadIsNotDescribedToTheOrganiserEither(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	store.policyErr = errors.New(
		`read the contest's SQL policy: failed to connect to ` +
			"`user=dbcontest database=dbcontest_core`: [::1]:5432: server closed the connection")
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `SELECT 1`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}

	built, err := service.Build(t.Context(), time.Minute)
	if err == nil {
		t.Fatal("a core-database failure was reported as a clean tick")
	}
	if built.BuildError != provisioning.BuildFailedInternally ||
		store.finished[0].err != provisioning.BuildFailedInternally {
		t.Fatalf("served %q and recorded %q, want the fixed sentence", built.BuildError, store.finished[0].err)
	}
	if len(cluster.names) != 0 {
		t.Fatal("built a template with no policy to grant")
	}
}

// The privileges a participant gets inside the game are the contest's own,
// and the *first* build is the one most likely to get this wrong: the
// template is not 'ready' yet, so the pool's own Game() lookup has no row to
// answer from (CLAUDE.md rule 11).
func TestTheBuildGrantsTheContestsOwnPolicyAndNotTheDefault(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	writable := sqlpolicy.ReadOnly()
	writable.Mode = sqlpolicy.ModeReadWrite
	writable.WritableTables = []string{"notes"}
	store.policy = writable

	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `SELECT 1`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	if _, err := service.Build(t.Context(), time.Minute); err != nil {
		t.Fatalf("building: %v", err)
	}

	if cluster.policy.Mode != sqlpolicy.ModeReadWrite {
		t.Fatalf("built with mode %q, want the contest's own read_write", cluster.policy.Mode)
	}
}

// A tick with nothing waiting must not be an error the log shouts about.
func TestBuildingWithNothingWaitingSaysSoRatherThanFailing(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	store.claimErr = provisioning.ErrNoGame

	if _, err := service.Build(t.Context(), time.Minute); !errors.Is(err, provisioning.ErrNoGame) {
		t.Fatalf("answered %v, want ErrNoGame", err)
	}
	if len(cluster.names) != 0 {
		t.Fatal("built something with nothing claimed")
	}
}

// uploadsGames is games(editable), plus a real gamefile.Store backing a
// file-sourced game's build — the one thing games() itself never wires up
// (WithUploads is left uncalled there on purpose, for the tests about an
// installation with no upload volume configured at all).
func uploadsGames(t *testing.T, editable bool) (*provisioning.Games, *templateStore, *buildCluster, *gamefile.Store) {
	t.Helper()
	service, store, cluster := games(editable)
	limits := gamefile.Limits{MaxFileBytes: 1 << 20, MaxDirBytes: 1 << 20, MaxChunkBytes: 1 << 20}
	files, err := gamefile.NewStore(t.TempDir(), limits)
	if err != nil {
		t.Fatalf("opening the upload store: %v", err)
	}
	service.WithUploads(files, limits)
	return service, store, cluster, files
}

// sealedUpload writes dump to files under id, exactly as a completed browser
// upload would have left it (internal/gamefile.Store's own three-call
// lifecycle), so a build has real bytes on disk to open.
func sealedUpload(t *testing.T, files *gamefile.Store, id uuid.UUID, dump string) {
	t.Helper()
	if err := files.Begin(id.String(), 1<<16); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := files.Append(id.String(), 0, strings.NewReader(dump)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := files.Complete(id.String(), int64(len(dump))); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// The point of BuildTemplate taking an io.Reader (its own doc explains why):
// a file-sourced game's build opens the uploaded bytes straight off disk and
// runs them through the identical path an editor's script uses, rather than
// a second one that could drift from it.
func TestBuildingAFileSourcedGameStreamsTheUploadedFileThroughTheSameClusterPathAScriptUses(t *testing.T) {
	t.Parallel()
	service, store, cluster, files := uploadsGames(t, true)

	const dump = "CREATE TABLE guests (id int);\nINSERT INTO guests VALUES (1);\n"
	id := uuid.New()
	sealedUpload(t, files, id, dump)

	contest := uuid.New()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 1,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, UploadID: &id,
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("finished as %q, want ready: %s", built.Status, built.BuildError)
	}
	if len(cluster.scripts) != 1 || cluster.scripts[0] != dump {
		t.Fatalf("built with %v, want the uploaded file's own bytes %q", cluster.scripts, dump)
	}
	if len(store.finished) != 1 || store.finished[0].err != "" {
		t.Fatalf("recorded %+v", store.finished)
	}
}

// The other branch finishUploadBuild has in common with Build: PostgreSQL's
// verdict on the uploaded SQL is the organiser's to read, exactly as it is
// for a script written in the editor.
func TestAFileSourcedBuildThatFailsKeepsThePostgresErrorForTheOrganiser(t *testing.T) {
	t.Parallel()
	service, store, cluster, files := uploadsGames(t, true)
	cluster.fail = scriptRefusal{says: `the game script was refused: type "nosuchtype" does not exist (SQLSTATE 42704)`}

	id := uuid.New()
	sealedUpload(t, files, id, `CREATE TABLE oops (x nosuchtype);`)
	contest := uuid.New()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 1,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, UploadID: &id,
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("a script PostgreSQL refused was reported as the tick's own failure: %v", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}
	if !strings.Contains(built.BuildError, "nosuchtype") {
		t.Fatalf("build error = %q — PostgreSQL's own words are the useful ones", built.BuildError)
	}
}

// A redeploy that drops GAME_UPLOAD_DIR out from under a game still waiting
// to build is an installation fault, not the organiser's — games() itself
// never calls WithUploads, standing in for exactly that installation.
func TestBuildingAFileSourcedGameWithNoUploadVolumeConfiguredIsAnInternalFault(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	id := uuid.New()
	contest := uuid.New()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 3,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, UploadID: &id,
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err == nil {
		t.Fatal("a build with no upload volume configured was reported as a clean tick")
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}
	if built.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("build error = %q, want the fixed sentence", built.BuildError)
	}
	if len(cluster.names) != 0 {
		t.Fatal("a file-sourced game with no upload volume reached BuildTemplate")
	}
	if len(store.finished) != 1 || store.finished[0].err != provisioning.BuildFailedInternally {
		t.Fatalf("recorded %+v, want the fixed sentence", store.finished)
	}
}

// migration 24's own CHECK ties SourceFile to a non-nil UploadID; a row that
// somehow lacks one is corrupt, never something an organiser's upload did.
func TestBuildingAFileSourcedGameWithNoUploadIDIsAnInternalFault(t *testing.T) {
	t.Parallel()
	service, store, cluster, _ := uploadsGames(t, true)
	contest := uuid.New()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 3,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, // UploadID left nil
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err == nil {
		t.Fatal("a corrupt file-sourced row (no upload id) was reported as a clean tick")
	}
	if built.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("build error = %q, want the fixed sentence", built.BuildError)
	}
	if len(cluster.names) != 0 {
		t.Fatal("a row with no upload id reached BuildTemplate")
	}
}

// A builder-sourced game is built the same way an editor-sourced one is:
// Definition.SQL generates the CREATE TABLE statements and they reach
// BuildTemplate exactly like claimed.Script already does — the fake cluster
// cannot tell the two apart, which is the point (finishDefinitionBuild's own
// doc: there is no third path).
func TestBuildingABuilderSourcedGameGeneratesSQLAndRunsItThroughTheSamePathAsAScript(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	contest := uuid.New()
	definition := aDefinition()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 1,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder, Definition: definition,
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.Status != provisioning.TemplateReady {
		t.Fatalf("finished as %q: %s", built.Status, built.BuildError)
	}
	if len(cluster.names) != 1 || cluster.names[0] != "game_tpl_cabc" {
		t.Fatalf("built database(s) %v, want exactly one, game_tpl_cabc", cluster.names)
	}

	want, err := definition.SQL()
	if err != nil {
		t.Fatalf("generate the same SQL directly: %v", err)
	}
	if len(cluster.scripts) != 1 || cluster.scripts[0] != want {
		t.Fatalf("the cluster received:\n%s\nwant Definition.SQL's own output:\n%s", cluster.scripts, want)
	}
}

// A definition with no tables cannot be saved — Validate refuses it before a
// build could ever be claimed for it (Definition.Validate's own doc) — but a
// row that somehow reaches Build with one anyway must still refuse plainly,
// as the organiser's own mistake, rather than run an empty script and mark a
// tableless database 'ready'. Never BuildFailedInternally: this is not a
// fault of this installation's cluster, so err must come back nil, the same
// way finishDefinitionBuild treats a script PostgreSQL itself refused.
func TestBuildingABuilderSourcedGameWithNoTablesRefusesAsTheOrganisersOwnMistake(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	contest := uuid.New()
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_tpl_cabc", Version: 1,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder, Definition: provisioning.Definition{},
	}
	store.present = true

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("an empty definition was reported as the tick's own failure: %v", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}
	if built.BuildError != provisioning.ErrDefinitionEmpty.Error() {
		t.Fatalf("build error = %q, want the organiser's own %q", built.BuildError, provisioning.ErrDefinitionEmpty)
	}
	if len(cluster.names) != 0 {
		t.Fatal("an empty definition reached BuildTemplate")
	}
	if len(store.finished) != 1 || store.finished[0].err != provisioning.ErrDefinitionEmpty.Error() {
		t.Fatalf("recorded %+v, want the organiser's own message", store.finished)
	}
}

func TestTheScriptIsReadableForTheExportAndAContestWithoutOneIsNotAnError(t *testing.T) {
	// contests.GameSource, the narrow view the contest package's export asks
	// for. A contest whose game has not been written yet exports without one
	// rather than failing, so "no game" must not surface here as an error.
	service, _, _ := games(true)

	script, ok, omitted, err := service.Script(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("Script() on a contest with no game returned error: %v", err)
	}
	if ok || omitted || script != "" {
		t.Fatalf("Script() answered %q (present: %v, omitted: %v), want an absent game", script, ok, omitted)
	}

	contest := uuid.New()
	if _, err := service.SetScript(t.Context(), uuid.New(), contest, `CREATE TABLE suspects (id int);`); err != nil {
		t.Fatalf("SetScript() returned error: %v", err)
	}

	script, ok, omitted, err = service.Script(t.Context(), contest)
	if err != nil {
		t.Fatalf("Script() returned error: %v", err)
	}
	if !ok || omitted || script != `CREATE TABLE suspects (id int);` {
		t.Fatalf("Script() answered %q (present: %v, omitted: %v)", script, ok, omitted)
	}
}

// The other half of the same method, and the one the export was getting
// wrong: a game built from an uploaded dump has no script column to hand over
// — its SQL is the file on the API host's own volume — and "" was
// indistinguishable from a script an organiser had actually written. The
// contest package cannot tell the two apart itself (it does not know
// SourceFile exists), so this is where the fact has to be produced.
func TestAFileSourcedGameIsReportedAsPresentButOmittedRatherThanAsAnEmptyScript(t *testing.T) {
	t.Parallel()
	service, store, _, files := uploadsGames(t, true)
	contest := uuid.New()

	upload := uuid.New()
	if err := files.Begin(upload.String(), 1<<16); err != nil {
		t.Fatalf("begin the upload on disk: %v", err)
	}
	if _, err := store.BeginUpload(t.Context(), upload, contest, "dump.sql", 10); err != nil {
		t.Fatalf("begin the upload: %v", err)
	}
	if _, err := files.Append(upload.String(), 0, strings.NewReader("CREATE X;\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := service.CompleteUpload(t.Context(), uuid.New(), contest, upload); err != nil {
		t.Fatalf("complete the upload: %v", err)
	}

	script, ok, omitted, err := service.Script(t.Context(), contest)
	if err != nil {
		t.Fatalf("Script() returned error: %v", err)
	}
	if !ok {
		t.Fatal("a contest whose game is an uploaded dump answered that it has no game")
	}
	if !omitted {
		t.Fatal("a file-sourced game answered as if its script were in the row")
	}
	if script != "" {
		t.Fatalf("Script() answered %q for a game whose SQL is a file", script)
	}
}

// The same fact, for the third source: a builder-sourced game has no SQL at
// all yet (Definition's own doc), so Script() must report it as present but
// omitted rather than as an empty script an organiser supposedly wrote.
func TestABuilderSourcedGameIsReportedAsPresentButOmittedRatherThanAsAnEmptyScript(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("setting the definition: %v", err)
	}

	script, ok, omitted, err := service.Script(t.Context(), contest)
	if err != nil {
		t.Fatalf("Script() returned error: %v", err)
	}
	if !ok {
		t.Fatal("a contest whose game is a table-builder definition answered that it has no game")
	}
	if !omitted {
		t.Fatal("a builder-sourced game answered as if its script were in the row")
	}
	if script != "" {
		t.Fatalf("Script() answered %q for a game whose SQL does not exist yet", script)
	}
}

func TestAFailingGameStoreIsReportedRatherThanReadAsNoGame(t *testing.T) {
	// The difference matters: "no game" makes the export succeed with a
	// package that has none, so a storage failure quietly wearing that
	// answer would ship an incomplete package as a complete one.
	store := &templateStore{templateErr: errors.New("the database is away")}
	service := provisioning.NewGames(store, &buildCluster{}, authoring{editable: true})

	if _, _, _, err := service.Script(t.Context(), uuid.New()); err == nil {
		t.Fatal("Script() swallowed a storage failure")
	}
}

// TestNeedsBuildOnlyWhenReadyAndMarked is every state the predicate has to
// discriminate: only a `ready` template with a mark invites a rebuild. A
// build already under way (`pending`, `building`) will pick the row up on
// its own — offering to press a button that a build is already running
// towards is nonsense — and a `failed` template's own error is what an
// organiser needs to see next, not an invitation to replace it.
func TestNeedsBuildOnlyWhenReadyAndMarked(t *testing.T) {
	t.Parallel()
	mark := time.Now()
	for _, tc := range []struct {
		name   string
		status provisioning.TemplateStatus
		marked bool
		want   bool
	}{
		{"ready with a mark", provisioning.TemplateReady, true, true},
		{"ready without a mark", provisioning.TemplateReady, false, false},
		{"pending with a mark", provisioning.TemplatePending, true, false},
		{"building with a mark", provisioning.TemplateBuilding, true, false},
		{"failed with a mark", provisioning.TemplateFailed, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := provisioning.Template{Status: tc.status}
			if tc.marked {
				template.DataChangedAt = &mark
			}
			if got := template.NeedsBuild(); got != tc.want {
				t.Fatalf("NeedsBuild() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestABuildClearsAMarkItActuallySaw is the ordinary case: a row changed
// before the build was ever claimed, so the build's own read of the table
// data already included it, and the mark must not survive.
func TestABuildClearsAMarkItActuallySaw(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `SELECT 1`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	before := time.Now().Add(-time.Hour)
	store.template.DataChangedAt = &before

	if _, err := service.Build(t.Context(), time.Minute); err != nil {
		t.Fatalf("building: %v", err)
	}
	if store.template.DataChangedAt != nil {
		t.Fatalf("the mark survived a build that started well after it: %v", *store.template.DataChangedAt)
	}
}

// TestABuildLeavesAMarkThatAppearedAfterTheClaim is the race this feature
// exists for: a row typed while a build is already running must still be
// picked up by the next one.
//
// The fake cannot literally interleave a write with Games.Build's one call,
// so the mark is set to a timestamp an hour in the future before Build ever
// runs. That is the same situation a genuinely concurrent write would
// produce: FinishBuild's own comparison (postgres/gametemplates.go and this
// package's templateStore.FinishBuild) only ever looks at the two
// timestamps — the mark's and the claim's — never at which call happened
// first in wall-clock time. A mark timestamped after the claim behaves
// identically whether it was written a millisecond after the claim or,
// as here, deliberately stamped an hour ahead of it.
func TestABuildLeavesAMarkThatAppearedAfterTheClaim(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `SELECT 1`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	after := time.Now().Add(time.Hour)
	store.template.DataChangedAt = &after

	if _, err := service.Build(t.Context(), time.Minute); err != nil {
		t.Fatalf("building: %v", err)
	}
	if store.template.DataChangedAt == nil {
		t.Fatal("the build cleared a mark it could not have seen; that row will never be built")
	}
}

// TestRequestBuildPutsAReadyGameBackToPendingAndRaisesItsVersion is the button
// itself: the data is in, and the organiser asks for the database to be made
// again from it.
//
// The version has to rise. Every copy carries the version it was made from,
// and only a higher one makes the existing copies stale — a rebuild that left
// it alone would build a template nobody is ever given.
func TestRequestBuildPutsAReadyGameBackToPendingAndRaisesItsVersion(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	before, err := store.TemplateStatus(t.Context(), contest)
	if err != nil {
		t.Fatalf("read the template: %v", err)
	}
	markBuilt(store)

	asked, err := service.RequestBuild(t.Context(), uuid.New(), contest)
	if err != nil {
		t.Fatalf("RequestBuild: %v", err)
	}
	if asked.Status != provisioning.TemplatePending {
		t.Fatalf("the game is %q after a build was asked for, want %q", asked.Status, provisioning.TemplatePending)
	}
	if asked.Version <= before.Version {
		t.Fatalf("the version is %d, want more than %d: the copies made from the old one stay current",
			asked.Version, before.Version)
	}
}

// TestRequestBuildIsRefusedOnceTheContestIsRunning: raising the version drops
// and remakes every participant's copy, which in a running olympiad is every
// participant losing their database at once.
func TestRequestBuildIsRefusedOnceTheContestIsRunning(t *testing.T) {
	t.Parallel()
	service, store, _ := games(false)
	contest := uuid.New()
	store.present = true
	store.template = provisioning.Template{
		ContestID: contest, Database: "game_x", Version: 3,
		Status: provisioning.TemplateReady, Source: provisioning.SourceBuilder,
	}

	if _, err := service.RequestBuild(t.Context(), uuid.New(), contest); !errors.Is(err, provisioning.ErrGameNotEditable) {
		t.Fatalf("RequestBuild = %v, want ErrGameNotEditable", err)
	}
}

// TestRequestBuildIsRefusedWhileOneIsAlreadyRunning: two organisers on the
// same screen, or one impatient double click. The second is told so rather
// than racing CREATE DATABASE against the first.
func TestRequestBuildIsRefusedWhileOneIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	// SaveDefinition leaves the game 'pending': a build is already waiting.

	if _, err := service.RequestBuild(t.Context(), uuid.New(), contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
		t.Fatalf("RequestBuild = %v, want ErrBuildInProgress", err)
	}
}

// TestRequestBuildWithoutAGameSaysSo: nothing has been written to build.
func TestRequestBuildWithoutAGameSaysSo(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)

	if _, err := service.RequestBuild(t.Context(), uuid.New(), uuid.New()); !errors.Is(err, provisioning.ErrNoGame) {
		t.Fatalf("RequestBuild = %v, want ErrNoGame", err)
	}
}
