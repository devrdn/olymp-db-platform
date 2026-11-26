package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service tests use the real repository, since claiming and versioning are
// properties of the SQL. The cluster is faked; internal/gamedb tests the real one.
var testPool *pgxpool.Pool

// newService is the service over the real repository with an audit trail
// nobody reads, for tests that do not look at the entries.
func newService(cluster provisioning.Cluster) *provisioning.Service {
	return provisioning.New(postgres.NewGameInstances(testPool), cluster,
		audit.New(unreadTrail{}), storage.NewUnitOfWork(testPool))
}

// unreadTrail keeps no entries, so services with several workers can share it.
type unreadTrail struct{}

func (unreadTrail) Append(context.Context, audit.Entry) error       { return nil }
func (unreadTrail) AppendMany(context.Context, []audit.Entry) error { return nil }

// TestMain opens the pool through storagetest, which refuses any database that
// is not a test database, since these tests commit fixtures.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := storagetest.OpenCore(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot use the core test database: %v\n", err)
		os.Exit(1)
	}
	if pool == nil {
		os.Exit(m.Run())
	}
	testPool = pool

	// No game row that existed before the run may change status by the end:
	// a test must not damage rows it did not create.
	before := gameRowStatuses(ctx)

	code := m.Run()

	if damaged := statusesChangedSince(before); len(damaged) > 0 {
		fmt.Fprintf(os.Stderr,
			"these tests changed the status of %d database row(s) they did not create:\n", len(damaged))
		for _, line := range damaged {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
		fmt.Fprintln(os.Stderr,
			"a row marked 'dropped' over a database that is still on the game cluster is never "+
				"reclaimed again (Reclaimable skips it), so this is real damage to the installation — "+
				"see withRollback below")
		code = 1
	}

	pool.Close()
	os.Exit(code)
}

// settleFor is how old a row must be to count as pre-existing. Other packages
// share the test database and legitimately change rows they just created.
const settleFor = 10 * time.Second

// gameRowStatuses reads the status of every settled game row in the whole
// database (instances by db_name, templates by template_db), not only rows a
// test made, because the damage it catches is to other rows.
func gameRowStatuses(ctx context.Context) map[string]string {
	rows, err := testPool.Query(ctx, `
		SELECT db_name, status FROM game_instances WHERE created_at < $1
		UNION ALL
		SELECT template_db, status FROM game_templates WHERE updated_at < $1`,
		time.Now().Add(-settleFor))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	found := map[string]string{}
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
			os.Exit(1)
		}
		found[name] = status
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
		os.Exit(1)
	}
	return found
}

// statusesChangedSince names every row of before whose status has moved. A
// row that disappeared is ordinary cleanup and is not reported.
func statusesChangedSince(before map[string]string) []string {
	if len(before) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	after := gameRowStatuses(ctx)

	var damaged []string
	for name, was := range before {
		if now, still := after[name]; still && now != was {
			damaged = append(damaged, fmt.Sprintf("%s: %s -> %s", name, was, now))
		}
	}
	sort.Strings(damaged)
	return damaged
}

// cluster records what it was asked to do, and can be told to refuse.
type cluster struct {
	mu      sync.Mutex
	made    []string
	dropped []string
	fail    error

	// creating and peak make the worker bound observable.
	creating, peak int
	slow           time.Duration
	// live is which names hold a database; copyFail fails a copy after its
	// name was dropped, as a failed CREATE DATABASE does.
	live     map[string]bool
	copyFail error
	// afterDrop runs once the forcing Drop has removed a database.
	afterDrop func()

	// busy makes DropIdle answer "not dropped, no error" (a live connection);
	// failIdleDrop makes it answer a real error.
	busy         map[string]bool
	failIdleDrop map[string]error
	idleDropped  []string

	// sized and sizeCalls record DatabaseSizes calls, batches the length of
	// each; sizesFail makes it fail. sizeOf overrides the 1 MiB it reports
	// for a name, and gone leaves a name out, as a database dropped meanwhile.
	sized     []string
	sizeCalls int
	batches   []int
	sizesFail error
	sizeOf    map[string]int64
	gone      map[string]bool

	// dropFail makes the forcing Drop refuse.
	dropFail error

	// idleCalls records every DropIdle call and its outcome. Reclaim sweeps
	// the whole database, so a test asks about its own row (outcomeOf) rather
	// than trusting aggregate counts.
	idleCalls []idleCall

	// templateBytes, clusterBytes and clusterBytesFail stage the byte budget's
	// inputs; clusterReads counts ClusterBytes calls.
	templateBytes    int64
	clusterBytes     int64
	clusterBytesFail error
	clusterReads     int
	// templateReads counts DatabaseSize calls, which are expensive on a real
	// cluster, so tests assert how often they happen; templatesAsked names
	// them, and templateFail makes each fail.
	templateReads  int
	templatesAsked []string
	templateFail   error
	// hangSizes and hangCluster make DatabaseSizes and ClusterBytes wait for
	// their context to end, as a hung cluster does; clusterWaiting counts the
	// ClusterBytes calls doing so.
	hangSizes      bool
	hangCluster    bool
	clusterWaiting int
}

type idleCall struct {
	name    string
	dropped bool
	err     error
}

// CreateInstance drops instance first and then copies, as gamedb's does, so
// a copy that fails or is cancelled leaves no database under the name.
func (c *cluster) CreateInstance(ctx context.Context, _, instance string, _ sqlpolicy.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fail != nil {
		return c.fail
	}
	if c.live == nil {
		c.live = map[string]bool{}
	}
	c.live[instance] = false
	if c.copyFail != nil {
		return c.copyFail
	}

	c.creating++
	if c.creating > c.peak {
		c.peak = c.creating
	}
	slow := c.slow
	c.mu.Unlock()

	// Held open so concurrent creations overlap; cut short like a statement
	// whose context ended.
	var cancelled error
	select {
	case <-time.After(slow):
	case <-ctx.Done():
		cancelled = ctx.Err()
	}

	c.mu.Lock()
	c.creating--
	if cancelled != nil {
		return cancelled
	}
	c.live[instance] = true
	c.made = append(c.made, instance)
	return nil
}

// exists reports whether the cluster holds a database named name.
func (c *cluster) exists(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live[name]
}

// inFlight is how many copies are being made right now.
func (c *cluster) inFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.creating
}

func (c *cluster) highWater() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func (c *cluster) DatabaseSize(_ context.Context, name string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateReads++
	c.templatesAsked = append(c.templatesAsked, name)
	if c.templateFail != nil {
		return 0, c.templateFail
	}
	if c.templateBytes > 0 {
		return c.templateBytes, nil
	}
	return 1 << 20, nil
}

// templateReadsOf counts the DatabaseSize calls about name.
func (c *cluster) templateReadsOf(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, asked := range c.templatesAsked {
		if asked == name {
			n++
		}
	}
	return n
}

func (c *cluster) templateSizeReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.templateReads
}

func (c *cluster) clusterReadsWaiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusterWaiting
}

func (c *cluster) clusterByteReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusterReads
}

func (c *cluster) setTemplateBytes(size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateBytes = size
}

func (c *cluster) fillTo(used int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterBytes = used
}

func (c *cluster) ClusterBytes(ctx context.Context) (int64, error) {
	c.mu.Lock()
	c.clusterReads++
	if c.hangCluster {
		c.clusterWaiting++
		c.mu.Unlock()
		<-ctx.Done()
		c.mu.Lock()
		c.clusterWaiting--
		c.mu.Unlock()
		return 0, ctx.Err()
	}
	defer c.mu.Unlock()
	if c.clusterBytesFail != nil {
		return 0, c.clusterBytesFail
	}
	return c.clusterBytes, nil
}

func (c *cluster) DatabaseSizes(ctx context.Context, names []string) (map[string]int64, error) {
	c.mu.Lock()
	hang := c.hangSizes
	c.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.sized = append(c.sized, names...)
	c.sizeCalls++
	c.batches = append(c.batches, len(names))
	if c.sizesFail != nil {
		return nil, c.sizesFail
	}
	sizes := make(map[string]int64, len(names))
	for _, name := range names {
		switch size, set := c.sizeOf[name]; {
		case c.gone[name]:
		case set:
			sizes[name] = size
		default:
			sizes[name] = 1 << 20
		}
	}
	return sizes, nil
}

// setSize makes DatabaseSizes report size for name, and present again.
func (c *cluster) setSize(name string, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sizeOf == nil {
		c.sizeOf = map[string]int64{}
	}
	c.sizeOf[name] = size
	delete(c.gone, name)
}

// vanish makes DatabaseSizes leave name out.
func (c *cluster) vanish(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone == nil {
		c.gone = map[string]bool{}
	}
	c.gone[name] = true
}

func (c *cluster) sizeBatches() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.batches...)
}

func (c *cluster) sizeReads() (names []string, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sized...), c.sizeCalls
}

func (c *cluster) Drop(ctx context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// As pgx refuses a statement on a context that has already ended.
	if err := ctx.Err(); err != nil {
		return err
	}

	if c.dropFail != nil {
		return c.dropFail
	}
	if c.live != nil {
		c.live[name] = false
	}
	c.dropped = append(c.dropped, name)
	if c.afterDrop != nil {
		c.afterDrop()
	}
	return nil
}

// droppedByForce reports whether the forcing Drop, not DropIdle, removed name.
func droppedByForce(c *cluster, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, dropped := range c.dropped {
		if dropped == name {
			return true
		}
	}
	return false
}

func (c *cluster) counts() (made, dropped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.made), len(c.dropped)
}

// DropIdle records successes in idleDropped, apart from dropped, so Reclaim's
// drops can be told from Invalidate's.
func (c *cluster) DropIdle(_ context.Context, name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err, ok := c.failIdleDrop[name]; ok {
		c.idleCalls = append(c.idleCalls, idleCall{name: name, err: err})
		return false, err
	}
	if c.busy[name] {
		c.idleCalls = append(c.idleCalls, idleCall{name: name})
		return false, nil
	}
	c.idleDropped = append(c.idleDropped, name)
	c.idleCalls = append(c.idleCalls, idleCall{name: name, dropped: true})
	return true, nil
}

// outcomeOf returns DropIdle's last answer about name, and whether it was
// asked at all.
func (c *cluster) outcomeOf(name string) (dropped bool, err error, called bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.idleCalls) - 1; i >= 0; i-- {
		if c.idleCalls[i].name == name {
			return c.idleCalls[i].dropped, c.idleCalls[i].err, true
		}
	}
	return false, nil, false
}

func (c *cluster) idleDrops() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.idleDropped...)
}

func (c *cluster) markBusy(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy == nil {
		c.busy = map[string]bool{}
	}
	c.busy[name] = true
}

func (c *cluster) failIdleDropOf(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failIdleDrop == nil {
		c.failIdleDrop = map[string]error{}
	}
	c.failIdleDrop[name] = err
}

var errRollback = errors.New("rolling back the test transaction")

// withRollback runs body inside a core-database transaction that is always
// rolled back, and hands body that transaction's context.
//
// Reclaim sweeps every reclaimable row in the database, not only the test's,
// and a row wrongly marked 'dropped' is never reclaimed again. The service
// joins this transaction through storage.QuerierFrom, so the sweep runs the
// production path (CLAUDE.md rule 10) while none of its writes survive.
func withRollback(t *testing.T, body func(ctx context.Context)) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}

	err := storage.NewUnitOfWork(testPool).Do(context.Background(), func(ctx context.Context) error {
		body(ctx)
		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("test transaction failed: %v", err)
	}
}

// contestFor sets up a contest and the given number of registrations, removed
// again when the test ends. It writes through storage.QuerierFrom, so inside
// withRollback the fixture shares the test's transaction.
func contestFor(t *testing.T, ctx context.Context, registrations int) (provisioning.Contest, []uuid.UUID) {
	t.Helper()

	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	q := storage.QuerierFrom(ctx, testPool)

	var author uuid.UUID
	err := q.QueryRow(ctx,
		`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Author', 'x')
		 RETURNING id`, "prov-author-"+uuid.NewString()[:8]).Scan(&author)
	if err != nil {
		t.Fatalf("create author: %v", err)
	}

	var id uuid.UUID
	if err := q.QueryRow(ctx,
		`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author).Scan(&id); err != nil {
		t.Fatalf("create contest: %v", err)
	}
	// Only does anything outside withRollback.
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
		_, _ = testPool.Exec(clean, `DELETE FROM users WHERE id = $1`, author)
	})

	var people []uuid.UUID
	for range registrations {
		var user, registration uuid.UUID
		if err := q.QueryRow(ctx,
			`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Player', 'x')
			 RETURNING id`, "prov-player-"+uuid.NewString()[:8]).Scan(&user); err != nil {
			t.Fatalf("create player: %v", err)
		}
		if err := q.QueryRow(ctx,
			`INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2) RETURNING id`,
			id, user).Scan(&registration); err != nil {
			t.Fatalf("create registration: %v", err)
		}
		people = append(people, registration)
	}

	return provisioning.Contest{
		ID: id, Template: "game_tpl_test", Version: 1, Policy: sqlpolicy.ReadOnly(),
	}, people
}

// markTemplateReady stores the 'ready' template row a successful build would
// leave. It is removed by contestFor's cleanup through the cascade.
func markTemplateReady(t *testing.T, ctx context.Context, contest uuid.UUID, database string) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`INSERT INTO game_templates (contest_id, template_db, init_script, status, version)
		 VALUES ($1, $2, 'SELECT 1', 'ready', 1)`, contest, database); err != nil {
		t.Fatalf("create template: %v", err)
	}
}

// templateStore is a contest's game in memory, its build state and the rows
// of both kinds of upload (stores), so a test controls its starting state and
// reads what the service left behind. Its upload rows are held to the real
// ones by TestUploadRowsInMemoryKeepThePostgresContract.
type templateStore struct {
	mu       sync.Mutex
	template provisioning.Template
	present  bool
	policy   sqlpolicy.Policy
	claims   int
	claimErr error
	// templateErr and policyErr, when set, are storage failures returned by
	// Template and Policy.
	templateErr error
	policyErr   error
	finished    []finish
	// described records each SaveSchema; describeErr makes it fail.
	described   []described
	describeErr error
	uploads     map[uuid.UUID]provisioning.Upload
	tableData   map[uuid.UUID]provisioning.TableData
	// progressWrites counts UpdateReceived calls of either kind, so a test
	// can tell a retried chunk recorded nothing.
	progressWrites int
	// completedBeforeAbort, when set, completes the upload an Abort is about
	// to mark, as a completion that commits between the abort's read and its
	// write does.
	completedBeforeAbort bool
	// ownerOfAll, when set, is the account every contest belongs to.
	ownerOfAll uuid.UUID
	// deleteRowErr, when set, is what the table rows' DeleteRow answers. The
	// fake does not reimplement the database's CHECK on deleted_rows; the
	// postgres tests prove that one.
	deleteRowErr error
	// tableDataReads, when armed, holds the table rows' Ready callers until the
	// expected number arrive, making two simultaneous "add row" requests
	// deterministic.
	tableDataReads *arrivalGate
}

// arrivalGate releases every caller at once, as soon as the expected number
// of them have arrived. A caller arriving after that passes straight through,
// so a gate armed for one part of a test does not hold up the rest of it.
type arrivalGate struct {
	mu        sync.Mutex
	remaining int
	open      chan struct{}
}

func (g *arrivalGate) arrive() {
	g.mu.Lock()
	if g.remaining > 0 {
		g.remaining--
		if g.remaining == 0 {
			close(g.open)
		}
	}
	g.mu.Unlock()
	<-g.open
}

func (s *templateStore) gateTableDataReads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tableDataReads = &arrivalGate{remaining: n, open: make(chan struct{})}
}

// directly is a unit of work with no transaction behind it.
type directly struct{}

func (directly) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type finish struct {
	version int
	err     string
}

// described is one SaveSchema call, and how many builds had finished by then.
type described struct {
	version       int
	schema        provisioning.Schema
	finishedFirst int
}

func (s *templateStore) SaveSchema(_ context.Context, _ uuid.UUID, version int, schema provisioning.Schema) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.describeErr != nil {
		return s.describeErr
	}
	s.described = append(s.described, described{version: version, schema: schema, finishedFirst: len(s.finished)})
	return nil
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
		Status: provisioning.TemplatePending, Source: provisioning.SourceEditor, Script: script,
	}
	s.present = true
	return s.template, nil
}

func (s *templateStore) SaveDefinition(
	_ context.Context, contestID uuid.UUID, database string, definition provisioning.Definition,
) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder, Definition: definition,
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

// TemplateStatus matches the real query: the script's length in place of the
// script, and no definition.
func (s *templateStore) TemplateStatus(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := s.Template(ctx, contestID)
	if err != nil {
		return provisioning.Template{}, err
	}
	template.ScriptBytes = len(template.Script)
	template.Script, template.Definition = "", provisioning.Definition{}
	return template, nil
}

func (s *templateStore) ClaimBuild(context.Context, time.Duration) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return provisioning.Template{}, s.claimErr
	}
	s.claims++
	s.template.UpdatedAt = time.Now()
	claimed := s.template
	claimed.Status = provisioning.TemplateBuilding
	return claimed, nil
}

func (s *templateStore) FinishBuild(
	_ context.Context, _ uuid.UUID, version int, buildError string, claimedAt time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, finish{version: version, err: buildError})
	// Mirrors the real repository: only a successful build clears the mark,
	// and only when nothing moved it past the claim.
	if buildError == "" && s.template.DataChangedAt != nil && !s.template.DataChangedAt.After(claimedAt) {
		s.template.DataChangedAt = nil
	}
	return nil
}

// markBuilt puts the template where a successful build leaves it.
func markBuilt(store *templateStore) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.template.Status = provisioning.TemplateReady
}

func (s *templateStore) MarkTableDataChanged(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now()
	s.template.DataChangedAt = &at
	return nil
}

func (s *templateStore) RequestBuild(_ context.Context, _ uuid.UUID, version int) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if s.template.Version != version ||
		s.template.Status != provisioning.TemplateReady && s.template.Status != provisioning.TemplateFailed {
		return provisioning.Template{}, provisioning.ErrBuildInProgress
	}
	s.template.Status = provisioning.TemplatePending
	s.template.Version++
	s.template.BuildError = ""
	return s.template, nil
}

func (s *templateStore) Policy(context.Context, uuid.UUID) (sqlpolicy.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyErr != nil {
		return sqlpolicy.Policy{}, s.policyErr
	}
	return s.policy, nil
}

// stores is the three stores this fake plays: the game's build state, its
// dumps' rows and its tables' rows, all held under one lock so the build
// state's CompleteUpload and the dump rows' InUse see each other.
func (s *templateStore) stores() provisioning.GameStores {
	return provisioning.GameStores{Builds: s, Dumps: s.dumpRows(), TableData: s.tableRows()}
}

func (s *templateStore) dumpRows() dumpRows   { return dumpRows{s} }
func (s *templateStore) tableRows() tableRows { return tableRows{s} }

// ownerOf answers ownerOfAll when set; otherwise each contest is its own
// owner, so no two of them share one.
func (s *templateStore) ownerOf(contestID uuid.UUID) uuid.UUID {
	if s.ownerOfAll != uuid.Nil {
		return s.ownerOfAll
	}
	return contestID
}

// ageUpload moves one upload's last activity, a dump's or a table's, back by
// by, past a janitor cutoff.
func (s *templateStore) ageUpload(id uuid.UUID, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.uploads[id]; ok {
		u.UpdatedAt = u.UpdatedAt.Add(-by)
		s.uploads[id] = u
	}
	if d, ok := s.tableData[id]; ok {
		d.UpdatedAt = d.UpdatedAt.Add(-by)
		s.tableData[id] = d
	}
}

// progressRecorded is how many times either kind's UpdateReceived was called.
func (s *templateStore) progressRecorded() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progressWrites
}

func (s *templateStore) CompleteUpload(
	_ context.Context, contestID, id uuid.UUID, database string, summary provisioning.UploadSummary, previous *uuid.UUID,
) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok || u.Status != provisioning.UploadReceiving {
		return provisioning.Template{}, provisioning.ErrUploadAlreadyComplete
	}
	u.Status, u.SHA256, u.Lines, u.ReceivedBytes = provisioning.UploadComplete, summary.SHA256, summary.Lines, summary.Bytes
	u.UpdatedAt = time.Now()
	s.uploads[id] = u
	if previous != nil {
		if p, ok := s.uploads[*previous]; ok {
			p.Status, p.UpdatedAt = provisioning.UploadAborted, time.Now()
			s.uploads[*previous] = p
		}
	}

	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, UploadID: &id,
	}
	s.present = true
	return s.template, nil
}

// dumpRows is provisioning.DumpRows over templateStore's uploads.
type dumpRows struct{ s *templateStore }

// Begin stamps the row like the column's DEFAULT now(): a zero time would make
// every row look abandoned to the janitor.
func (d dumpRows) Begin(_ context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	for _, u := range d.s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return provisioning.Upload{}, provisioning.ErrUploadInProgress
		}
	}
	if d.s.uploads == nil {
		d.s.uploads = map[uuid.UUID]provisioning.Upload{}
	}
	now := time.Now()
	u := provisioning.Upload{
		ID: id, ContestID: contestID, Filename: filename, DeclaredBytes: declaredBytes,
		Status: provisioning.UploadReceiving, CreatedAt: now, UpdatedAt: now,
	}
	d.s.uploads[id] = u
	return u, nil
}

func (d dumpRows) ByID(_ context.Context, id uuid.UUID) (provisioning.Upload, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	u, ok := d.s.uploads[id]
	if !ok {
		return provisioning.Upload{}, provisioning.ErrUploadNotFound
	}
	return u, nil
}

func (d dumpRows) Current(_ context.Context, contestID uuid.UUID) (provisioning.Upload, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	for _, u := range d.s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return u, nil
		}
	}
	return provisioning.Upload{}, provisioning.ErrUploadNotFound
}

func (d dumpRows) UpdateReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	d.s.progressWrites++
	u, ok := d.s.uploads[id]
	if !ok || u.Status != provisioning.UploadReceiving {
		return provisioning.ErrUploadAlreadyComplete
	}
	u.ReceivedBytes, u.UpdatedAt = max(u.ReceivedBytes, receivedBytes), time.Now()
	d.s.uploads[id] = u
	return nil
}

func (d dumpRows) Abort(_ context.Context, id uuid.UUID) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	u, ok := d.s.uploads[id]
	if ok && d.s.completedBeforeAbort {
		u.Status = provisioning.UploadComplete
		d.s.uploads[id] = u
	}
	if !ok || u.Status != provisioning.UploadReceiving {
		return provisioning.ErrUploadAlreadyComplete
	}
	u.Status, u.UpdatedAt = provisioning.UploadAborted, time.Now()
	d.s.uploads[id] = u
	return nil
}

func (d dumpRows) Abandoned(_ context.Context, cutoff time.Time, limit int) ([]provisioning.Upload, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	var out []provisioning.Upload
	for _, u := range d.s.uploads {
		if u.Status == provisioning.UploadReceiving && u.UpdatedAt.Before(cutoff) {
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out[:min(len(out), limit)], nil
}

func (d dumpRows) InUse(_ context.Context, id uuid.UUID) (bool, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	u, ok := d.s.uploads[id]
	if !ok {
		return false, nil
	}
	if u.Status == provisioning.UploadReceiving {
		return true, nil
	}
	return d.s.present && d.s.template.UploadID != nil && *d.s.template.UploadID == id, nil
}

// tableRows is provisioning.TableDataRows over templateStore's tableData.
type tableRows struct{ s *templateStore }

// Begin stamps the row as dumpRows.Begin does.
func (d tableRows) Begin(_ context.Context, id, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	for _, row := range d.s.tableData {
		if row.ContestID == contestID && strings.EqualFold(row.Table, table) && row.Status == provisioning.UploadReceiving {
			return provisioning.TableData{}, provisioning.ErrUploadInProgress
		}
	}
	if d.s.tableData == nil {
		d.s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	now := time.Now()
	row := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: declaredBytes,
		Status: provisioning.UploadReceiving, CreatedAt: now, UpdatedAt: now,
	}
	d.s.tableData[id] = row
	return row, nil
}

func (d tableRows) ByID(_ context.Context, id uuid.UUID) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	row, ok := d.s.tableData[id]
	if !ok {
		return provisioning.TableData{}, provisioning.ErrUploadNotFound
	}
	return row, nil
}

func (d tableRows) WithOwner(ctx context.Context, id uuid.UUID) (provisioning.TableData, uuid.UUID, error) {
	row, err := d.ByID(ctx, id)
	if err != nil {
		return provisioning.TableData{}, uuid.Nil, err
	}
	return row, d.s.ownerOf(row.ContestID), nil
}

func (d tableRows) Current(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	for _, row := range d.s.tableData {
		if row.ContestID == contestID && strings.EqualFold(row.Table, table) && row.Status == provisioning.UploadReceiving {
			return row, nil
		}
	}
	return provisioning.TableData{}, provisioning.ErrUploadNotFound
}

func (d tableRows) Ready(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	d.s.mu.Lock()
	gate := d.s.tableDataReads
	found, err := provisioning.TableData{}, error(provisioning.ErrUploadNotFound)
	for _, row := range d.s.tableData {
		if row.ContestID == contestID && strings.EqualFold(row.Table, table) && row.Status == provisioning.UploadComplete {
			found, err = row, nil
			break
		}
	}
	d.s.mu.Unlock()

	// Released outside the lock, after the answer is taken, so every held
	// caller leaves with the same snapshot.
	if gate != nil {
		gate.arrive()
	}
	return found, err
}

func (d tableRows) UpdateReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	d.s.progressWrites++
	row, ok := d.s.tableData[id]
	if !ok || row.Status != provisioning.UploadReceiving {
		return provisioning.ErrUploadAlreadyComplete
	}
	row.ReceivedBytes, row.UpdatedAt = max(row.ReceivedBytes, receivedBytes), time.Now()
	d.s.tableData[id] = row
	return nil
}

func (d tableRows) Complete(
	_ context.Context, _ uuid.UUID, _ string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID,
) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	row, ok := d.s.tableData[id]
	if !ok || row.Status != provisioning.UploadReceiving {
		return provisioning.TableData{}, provisioning.ErrUploadAlreadyComplete
	}
	row.Status, row.ReceivedBytes, row.Lines, row.UpdatedAt = provisioning.UploadComplete, receivedBytes, lines, time.Now()
	d.s.tableData[id] = row
	if previous != nil {
		if p, ok := d.s.tableData[*previous]; ok {
			p.Status, p.UpdatedAt = provisioning.UploadAborted, time.Now()
			d.s.tableData[*previous] = p
		}
	}
	return row, nil
}

func (d tableRows) CreateReady(
	_ context.Context, id, contestID uuid.UUID, table string, bytes, lines int64,
) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	for _, row := range d.s.tableData {
		if row.ContestID == contestID && strings.EqualFold(row.Table, table) && row.Status == provisioning.UploadComplete {
			return provisioning.TableData{}, provisioning.ErrUploadInProgress
		}
	}
	if d.s.tableData == nil {
		d.s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	now := time.Now()
	row := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: bytes, ReceivedBytes: bytes, Lines: lines,
		Status: provisioning.UploadComplete, CreatedAt: now, UpdatedAt: now,
	}
	d.s.tableData[id] = row
	return row, nil
}

func (d tableRows) AppendRow(_ context.Context, id uuid.UUID, at, rowBytes int64) (provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	row, ok := d.s.tableData[id]
	if !ok || row.Status != provisioning.UploadComplete || row.ReceivedBytes != at {
		return provisioning.TableData{}, provisioning.ErrTableDataChanged
	}
	row.ReceivedBytes, row.Lines = row.ReceivedBytes+rowBytes, row.Lines+1
	d.s.tableData[id] = row
	return row, nil
}

func (d tableRows) Abort(_ context.Context, id uuid.UUID) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	row, ok := d.s.tableData[id]
	if ok && d.s.completedBeforeAbort {
		row.Status = provisioning.UploadComplete
		d.s.tableData[id] = row
	}
	if !ok || row.Status != provisioning.UploadReceiving {
		return provisioning.ErrUploadAlreadyComplete
	}
	row.Status, row.UpdatedAt = provisioning.UploadAborted, time.Now()
	d.s.tableData[id] = row
	return nil
}

func (d tableRows) Discard(_ context.Context, contestID uuid.UUID) ([]uuid.UUID, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	var ids []uuid.UUID
	for id, row := range d.s.tableData {
		if row.ContestID != contestID {
			continue
		}
		if row.Status != provisioning.UploadReceiving && row.Status != provisioning.UploadComplete {
			continue
		}
		row.Status, row.UpdatedAt = provisioning.UploadAborted, time.Now()
		d.s.tableData[id] = row
		ids = append(ids, id)
	}
	return ids, nil
}

func (d tableRows) DeleteRow(_ context.Context, id uuid.UUID, row int64) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	data, ok := d.s.tableData[id]
	if !ok {
		return provisioning.ErrUploadNotFound
	}
	if d.s.deleteRowErr != nil {
		return d.s.deleteRowErr
	}
	for _, r := range data.DeletedRows {
		if r == row {
			return provisioning.ErrTableRowAlreadyDeleted
		}
	}
	data.DeletedRows = append(append([]int64{}, data.DeletedRows...), row)
	d.s.tableData[id] = data
	return nil
}

func (d tableRows) Abandoned(_ context.Context, cutoff time.Time, limit int) ([]provisioning.TableData, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	var out []provisioning.TableData
	for _, row := range d.s.tableData {
		if row.Status == provisioning.UploadReceiving && row.UpdatedAt.Before(cutoff) {
			out = append(out, row)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	return out[:min(len(out), limit)], nil
}

func (d tableRows) InUse(_ context.Context, id uuid.UUID) (bool, error) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	row, ok := d.s.tableData[id]
	if !ok {
		return false, nil
	}
	return row.Status == provisioning.UploadReceiving || row.Status == provisioning.UploadComplete, nil
}

// buildCluster records what it was asked to build and can be told to refuse.
type buildCluster struct {
	mu      sync.Mutex
	names   []string
	scripts []string
	policy  sqlpolicy.Policy
	fail    error
	// tableData records LoadTableData calls keyed "database.table";
	// tableDataFail makes it fail.
	tableData     map[string]string
	tableDataFail error
	dropped       []string
	// schema is what ReadSchema describes, schemaErr makes it fail, and
	// readSchemaOf records the databases it was asked about.
	schema       provisioning.Schema
	schemaErr    error
	readSchemaOf []string
	// missing names the databases MissingDatabases reports absent; every
	// other name exists. lookups records each name it was asked about, and
	// lookupErr makes it fail.
	missing   map[string]bool
	lookups   []string
	lookupErr error
	// duringLookup, when set, runs inside MissingDatabases, standing for
	// whatever happens elsewhere while the cluster is being asked.
	duringLookup func()
}

func (c *buildCluster) BuildTemplate(_ context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error {
	// Consumed in full, as a real build would, to record the bytes that
	// reached it.
	data, err := io.ReadAll(script)
	if err != nil {
		return fmt.Errorf("buildCluster: read script: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.names, c.scripts, c.policy = append(c.names, name), append(c.scripts, string(data)), policy
	return c.fail
}

func (c *buildCluster) LoadTableData(_ context.Context, database, table string, columns []string, data io.Reader) error {
	body, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("buildCluster: read table data: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tableDataFail != nil {
		return c.tableDataFail
	}
	if c.tableData == nil {
		c.tableData = map[string]string{}
	}
	c.tableData[database+"."+table] = string(body)
	return nil
}

func (c *buildCluster) ReadSchema(_ context.Context, database string) (provisioning.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readSchemaOf = append(c.readSchemaOf, database)
	return c.schema, c.schemaErr
}

func (c *buildCluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped = append(c.dropped, name)
	return nil
}

func (c *buildCluster) MissingDatabases(_ context.Context, names []string) ([]string, error) {
	if c.duringLookup != nil {
		c.duringLookup()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups = append(c.lookups, names...)
	if c.lookupErr != nil {
		return nil, c.lookupErr
	}
	var absent []string
	for _, name := range names {
		if c.missing[name] {
			absent = append(absent, name)
		}
	}
	return absent, nil
}

// asked is every name MissingDatabases was asked about.
func (c *buildCluster) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lookups)
}

// authoring stands for the contest's status: editable is a draft or published
// contest, running a running one, and neither a finished or archived one.
type authoring struct {
	editable bool
	running  bool
	// err fails GameEditable, runningErr ContestRunning.
	err        error
	runningErr error
}

func (a authoring) GameEditable(context.Context, uuid.UUID) (bool, error) { return a.editable, a.err }

func (a authoring) ContestRunning(context.Context, uuid.UUID) (bool, error) {
	return a.running, a.runningErr
}

func games(editable bool) (*provisioning.Games, *templateStore, *buildCluster) {
	return gamesOf(authoring{editable: editable})
}

func gamesOf(author authoring) (*provisioning.Games, *templateStore, *buildCluster) {
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	cluster := &buildCluster{}
	return provisioning.NewGames(store.stores(), cluster, author, noContest{}, directly{}), store, cluster
}

// onPostgres is every store a contest's game keeps, on the test database, with
// builds as the build state.
func onPostgres(builds provisioning.BuildRepository) provisioning.GameStores {
	return provisioning.GameStores{
		Builds: builds, Dumps: postgres.NewGameUploads(testPool), TableData: postgres.NewGameTableData(testPool),
	}
}

// ownGame is the real repository with ClaimBuild kept to one contest's game.
// ClaimBuild takes the oldest claimable game installation-wide, and packages
// tested in parallel share the database, so a plain claim here can build
// another package's game.
type ownGame struct {
	*postgres.GameInstances
	contest uuid.UUID
}

func ownBuilds(contest uuid.UUID) *ownGame {
	return &ownGame{GameInstances: postgres.NewGameInstances(testPool), contest: contest}
}

// ClaimBuild first ages this test's pending game by seven years, so the real
// claim reaches it first: older than the five years anotherPackagesPendingGame
// gives its game, younger than the ten the postgres claim tests give their own
// rows. If another claim holds the game for a moment, a foreign game claimed
// meanwhile is put back to pending (the only claimable status other packages
// commit) and the claim is tried again.
func (o *ownGame) ClaimBuild(ctx context.Context, stale time.Duration) (provisioning.Template, error) {
	if _, err := testPool.Exec(ctx,
		`UPDATE game_templates SET updated_at = now() - interval '7 years'
		 WHERE contest_id = $1 AND status = 'pending'`, o.contest); err != nil {
		return provisioning.Template{}, fmt.Errorf("age this test's game: %w", err)
	}
	for range 20 {
		claimed, err := o.GameInstances.ClaimBuild(ctx, stale)
		switch {
		case errors.Is(err, provisioning.ErrNoGame):
		case err != nil || claimed.ContestID == o.contest:
			return claimed, err
		default:
			if _, err := testPool.Exec(ctx,
				`UPDATE game_templates SET status = 'pending'
				 WHERE contest_id = $1 AND version = $2 AND status = 'building'`,
				claimed.ContestID, claimed.Version); err != nil {
				return provisioning.Template{}, fmt.Errorf("put back another package's game: %w", err)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return provisioning.Template{}, fmt.Errorf("twenty claims did not reach contest %s's game", o.contest)
}

// The postgres package commits an aged pending game while this package runs
// (anotherPackagesPendingGame). A build in these tests must still build the
// test's own game, or it fails whenever that game's contest is deleted mid-build.
func TestABuildInTheseTestsTakesItsOwnGameEvenWhenAnotherPackagesGameIsOlder(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	other, _ := contestFor(t, t.Context(), 0)
	repo := postgres.NewGameInstances(testPool)
	if _, err := repo.SaveScript(t.Context(), other.ID, "game_tpl_c"+uuid.NewString()[:12], `SELECT 1`); err != nil {
		t.Fatalf("save the other package's game: %v", err)
	}
	if _, err := testPool.Exec(t.Context(),
		`UPDATE game_templates SET updated_at = now() - interval '5 years' WHERE contest_id = $1`, other.ID); err != nil {
		t.Fatalf("age the other package's game: %v", err)
	}

	games := provisioning.NewGames(onPostgres(ownBuilds(contest.ID)), &buildCluster{}, editableContest{}, noContest{}, directly{})
	if _, err := games.SetScript(t.Context(), uuid.New(), contest.ID, nil, `SELECT 1`); err != nil {
		t.Fatalf("save this test's game: %v", err)
	}

	built, err := games.Build(t.Context(), time.Minute)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if built.ContestID != contest.ID {
		t.Fatalf("built contest %s's game, want this test's own (%s)", built.ContestID, contest.ID)
	}
	var status string
	if err := testPool.QueryRow(t.Context(),
		`SELECT status FROM game_templates WHERE contest_id = $1`, other.ID).Scan(&status); err != nil {
		t.Fatalf("read the other package's game: %v", err)
	}
	if status != string(provisioning.TemplatePending) {
		t.Fatalf("the other package's game is %q, want it left pending for its owner", status)
	}
}

// noContest is contests.ContentRows for a game whose contest has no row
// here: every edit finds a draft. The real one is tested in internal/postgres.
type noContest struct{}

func (noContest) LockContest(context.Context, uuid.UUID) error { return nil }

func (noContest) ByID(_ context.Context, id uuid.UUID) (contests.Contest, error) {
	return contests.Contest{ID: id, Status: contests.StatusDraft}, nil
}

func (noContest) MarkContentChanged(_ context.Context, read contests.Contest) (contests.Contest, error) {
	return read, nil
}
