package provisioning_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// templateStore is the game's own row, in memory, so a test can say exactly
// what state it starts in and read exactly what the service left behind.
type templateStore struct {
	mu       sync.Mutex
	template provisioning.Template
	present  bool
	policy   sqlpolicy.Policy
	claims   int
	claimErr error
	// templateErr, when set, is what Template returns instead of a row — a
	// storage failure rather than a contest that simply has no game.
	templateErr error
	// policyErr, when set, is what Policy returns: the core database refusing
	// the read the build makes before it touches the cluster.
	policyErr error
	finished  []finish
}

// directly is the unit of work for a test that wants the audit trail wired up
// without a transaction behind it. Build records outside any transaction
// anyway (its own doc says why), so this only satisfies the constructor.
type directly struct{}

func (directly) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type finish struct {
	version int
	err     string
}

func (s *templateStore) SaveScript(_ context.Context, contestID uuid.UUID, database, script string) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Script: script,
	}
	s.present = true
	return s.template, nil
}

func (s *templateStore) Template(context.Context, uuid.UUID) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateErr != nil {
		return provisioning.Template{}, s.templateErr
	}
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	return s.template, nil
}

func (s *templateStore) ClaimBuild(context.Context, time.Duration) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return provisioning.Template{}, s.claimErr
	}
	s.claims++
	claimed := s.template
	claimed.Status = provisioning.TemplateBuilding
	return claimed, nil
}

func (s *templateStore) FinishBuild(_ context.Context, _ uuid.UUID, version int, buildError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, finish{version: version, err: buildError})
	return nil
}

func (s *templateStore) Policy(context.Context, uuid.UUID) (sqlpolicy.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyErr != nil {
		return sqlpolicy.Policy{}, s.policyErr
	}
	return s.policy, nil
}

// buildCluster records what it was asked to build and can be told to refuse.
type buildCluster struct {
	mu      sync.Mutex
	names   []string
	scripts []string
	policy  sqlpolicy.Policy
	fail    error
}

func (c *buildCluster) BuildTemplate(_ context.Context, name, script string, policy sqlpolicy.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names, c.scripts, c.policy = append(c.names, name), append(c.scripts, script), policy
	return c.fail
}

type authoring struct {
	editable bool
	err      error
}

func (a authoring) GameEditable(context.Context, uuid.UUID) (bool, error) { return a.editable, a.err }

func games(editable bool) (*provisioning.Games, *templateStore, *buildCluster) {
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	cluster := &buildCluster{}
	return provisioning.NewGames(store, cluster, authoring{editable: editable}), store, cluster
}

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

func TestTheScriptIsReadableForTheExportAndAContestWithoutOneIsNotAnError(t *testing.T) {
	// contests.GameSource, the narrow view the contest package's export asks
	// for. A contest whose game has not been written yet exports without one
	// rather than failing, so "no game" must not surface here as an error.
	service, _, _ := games(true)

	script, ok, err := service.Script(t.Context(), uuid.New())
	if err != nil {
		t.Fatalf("Script() on a contest with no game returned error: %v", err)
	}
	if ok || script != "" {
		t.Fatalf("Script() answered %q (present: %v), want an absent game", script, ok)
	}

	contest := uuid.New()
	if _, err := service.SetScript(t.Context(), uuid.New(), contest, `CREATE TABLE suspects (id int);`); err != nil {
		t.Fatalf("SetScript() returned error: %v", err)
	}

	script, ok, err = service.Script(t.Context(), contest)
	if err != nil {
		t.Fatalf("Script() returned error: %v", err)
	}
	if !ok || script != `CREATE TABLE suspects (id int);` {
		t.Fatalf("Script() answered %q (present: %v)", script, ok)
	}
}

func TestAFailingGameStoreIsReportedRatherThanReadAsNoGame(t *testing.T) {
	// The difference matters: "no game" makes the export succeed with a
	// package that has none, so a storage failure quietly wearing that
	// answer would ship an incomplete package as a complete one.
	store := &templateStore{templateErr: errors.New("the database is away")}
	service := provisioning.NewGames(store, &buildCluster{}, authoring{editable: true})

	if _, _, err := service.Script(t.Context(), uuid.New()); err == nil {
		t.Fatal("Script() swallowed a storage failure")
	}
}
