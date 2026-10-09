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
	// The pending status keeps the build out of the saving request.
	if len(cluster.names) != 0 {
		t.Fatal("built the game inside the request that stored the script")
	}
	if store.template.Script == "" {
		t.Fatal("the script was not stored")
	}
}

// Replacing a game makes every copy stale, and in a running olympiad that
// drops every participant's database at once.
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

// aDefinition is a small valid game: a suspects table with a primary key.
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
	if len(cluster.names) != 0 {
		t.Fatal("built the game inside the request that stored the definition")
	}
	if len(store.template.Definition.Tables) == 0 {
		t.Fatal("the definition was not stored")
	}
}

// The table builder is refused the same way as the script editor.
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

// Only the wiring: definition_test.go covers each refusal.
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

// The trail must say which of the three sources built a game, and being
// append-only it cannot be filled in afterwards.
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
	// The trail records who did what; the version identifies the definition.
	for _, value := range entry.Payload {
		if _, isDefinition := value.(provisioning.Definition); isDefinition {
			t.Fatalf("the payload carries the whole definition: %+v", entry.Payload)
		}
	}
}

// A table's data is a CSV whose header names its columns in order, so once
// the table holds a row its structure is frozen; the builder screen applies
// the same freeze client-side.
func TestADefinitionChangeThatWouldOrphanATablesOwnDataIsRefused(t *testing.T) {
	t.Parallel()
	service, store, _ := games(true)
	contest := uuid.New()

	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the first definition: %v", err)
	}
	// Only the row recording three lines is needed, not a file on disk.
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
				// The primary key is dropped.
			}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.SetDefinition(t.Context(), uuid.New(), contest, tc.next)
			if !errors.Is(err, provisioning.ErrDefinitionTableLocked) {
				t.Fatalf("answered %v, want ErrDefinitionTableLocked", err)
			}
			if store.template.Version != 1 {
				t.Fatalf("version is %d after a refused save, want 1", store.template.Version)
			}
		})
	}
}

// The freeze is per table: a new table may still be added beside a locked one.
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

// A header with no rows is not locked: the build skips the header, so nothing
// it reads can disagree with a new structure.
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

// scriptRefusal stands for gamedb.ScriptError, the one build failure whose
// text the organiser may read. A bare errors.New would take the internal-fault
// branch instead.
type scriptRefusal struct{ says string }

func (s scriptRefusal) Error() string           { return s.says }
func (s scriptRefusal) ScriptRejection() string { return s.says }

// connectFailureText is the text of a real *pgconn.ConnectError from the game
// cluster: it names the role, the addresses and the database, none of which
// may reach a response body or an audit payload.
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

// A build that failed for our own reasons must not describe our cluster to the
// organiser: BuildError is served verbatim and the audit payload is append-only.
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

	if store.finished[0].err != provisioning.BuildFailedInternally {
		t.Fatalf("recorded %q, want the fixed sentence", store.finished[0].err)
	}
	if built.BuildError != provisioning.BuildFailedInternally {
		t.Fatalf("served %q, want the fixed sentence", built.BuildError)
	}

	// Saving the script recorded an entry too, so find the build's by action.
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

// A script refusal quotes the author's untrusted file back, so its size and
// bytes are theirs. build_error is an unbounded `text` column copied into the
// audit payload (CLAUDE.md rule 2), and a non-UTF-8 or NUL byte makes
// FinishBuild fail with 22021, leaving the row 'building' to be rebuilt every
// sweep. NUL is valid UTF-8 in Go, so it is checked on its own; a mistaken
// `pg_dump -Fc` upload is full of them.
func TestAScriptRefusalIsBoundedAndFitToStoreBeforeItIsStored(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	trail := &sink{}
	service = service.WithAudit(audit.New(trail), directly{})
	cluster.fail = scriptRefusal{says: "line 1: \xff\xfe\x00 " + strings.Repeat("q", 4<<20)}

	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `CREATE TABLE fine (x int);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	recordedEntry := ""
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
		// Truncation keeps the front, which is the useful part.
		if !strings.HasPrefix(text, "line 1: ") {
			t.Fatalf("%s = %q, and no longer starts with what the reader said", label, text)
		}
	}
}

// The policy read fails before the cluster is touched, and its error text
// describes the core database.
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

// On the first build the template is not 'ready', so the pool's Game() lookup
// has no row to take the policy from (CLAUDE.md rule 11).
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

// uploadsGames is games(editable) with a real upload store; games() leaves
// WithUploads uncalled to stand for an installation with no upload volume.
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

// sealedUpload leaves dump on disk under id as a completed upload would.
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

// The uploaded bytes go through the same BuildTemplate path as a script, not
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

// A redeploy without GAME_UPLOAD_DIR is an installation fault, not the
// organiser's; games() never calls WithUploads.
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

// A CHECK constraint ties SourceFile to an UploadID, so a row without one is
// corrupt, not the organiser's doing.
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

// Definition.SQL's output reaches BuildTemplate the same way a script does.
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

// Validate refuses an empty definition on save, but one that reaches Build
// anyway must fail as the organiser's mistake (err nil, not
// BuildFailedInternally) rather than mark a tableless database 'ready'.
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
	// The export (contests.GameSource) treats a missing game as absent, not as
	// a failure.
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

// A dump's SQL lives on disk, not in the row, and the contest package cannot
// tell that from an empty script, so Script() reports it as omitted.
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

// A builder-sourced game has no stored SQL at all.
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
	// Read as "no game", the export would ship an incomplete package.
	store := &templateStore{templateErr: errors.New("the database is away")}
	service := provisioning.NewGames(store, &buildCluster{}, authoring{editable: true})

	if _, _, _, err := service.Script(t.Context(), uuid.New()); err == nil {
		t.Fatal("Script() swallowed a storage failure")
	}
}

// A pending or building template will pick the data up on its own, and a
// failed one's error is what the organiser needs to see next.
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

// A row typed during a build must be picked up by the next one. The fake
// cannot interleave a write, so the mark is stamped an hour ahead: FinishBuild
// compares only the mark's and the claim's timestamps.
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

// A failed build drops its template, so the mark stays even though the
// timestamps say nothing moved: no database holds the rows it stands for.
func TestAFailedBuildLeavesTheDataMarkWhereItFoundIt(t *testing.T) {
	t.Parallel()
	service, store, cluster := games(true)
	cluster.fail = scriptRefusal{says: `the game script was refused: type "nosuchtype" does not exist (SQLSTATE 42704)`}
	if _, err := service.SetScript(t.Context(), uuid.New(), uuid.New(), `CREATE TABLE oops (x nosuchtype);`); err != nil {
		t.Fatalf("setting the script: %v", err)
	}
	before := time.Now().Add(-time.Hour)
	store.template.DataChangedAt = &before

	built, err := service.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if built.Status != provisioning.TemplateFailed {
		t.Fatalf("finished as %q, want failed", built.Status)
	}
	if store.template.DataChangedAt == nil {
		t.Fatal("a failed build cleared the data mark; the data is unbuilt and nothing records it any more")
	}
}

// Only a higher version makes existing copies stale; without it the rebuilt
// template would never be handed out.
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

// A second request (a double click, two organisers) must not race CREATE
// DATABASE against the first.
func TestRequestBuildIsRefusedWhileOneIsAlreadyRunning(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)
	contest := uuid.New()
	if _, err := service.SetDefinition(t.Context(), uuid.New(), contest, aDefinition()); err != nil {
		t.Fatalf("save the definition: %v", err)
	}
	// SetDefinition leaves the game 'pending': a build is already waiting.

	if _, err := service.RequestBuild(t.Context(), uuid.New(), contest); !errors.Is(err, provisioning.ErrBuildInProgress) {
		t.Fatalf("RequestBuild = %v, want ErrBuildInProgress", err)
	}
}

func TestRequestBuildWithoutAGameSaysSo(t *testing.T) {
	t.Parallel()
	service, _, _ := games(true)

	if _, err := service.RequestBuild(t.Context(), uuid.New(), uuid.New()); !errors.Is(err, provisioning.ErrNoGame) {
		t.Fatalf("RequestBuild = %v, want ErrNoGame", err)
	}
}
