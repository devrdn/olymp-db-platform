package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These run against the real repository, not a stand-in for it. A fake
// repository would agree with the service about claiming, versions and the
// composite reference, and every one of those is a property of the SQL — the
// two would drift the first time one of them was edited.
//
// The cluster, on the other hand, is faked: what it does is create and drop
// databases, which internal/gamedb tests against a real one. Here it is only
// asked what it was told to do.
var testPool *pgxpool.Pool

// TestMain opens the pool through storagetest, which refuses any database that
// is not a test database. These tests commit fixtures, and they used to commit
// them into the database `make run` serves the product from.
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

	// The standing net under every test in this package, not only the reclaim
	// ones: whatever a test does, no game database row that was already in the
	// database may have a different status when the package is done. See
	// gameRowStatuses below. storagetest now keeps these tests off the
	// developer's installation altogether; this still catches a test that
	// damages rows it did not create, which is a defect wherever the rows live.
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

// settleFor is how recently a row may have been created and still be treated
// as somebody else's work in progress rather than as part of the
// installation.
//
// `make test-db` runs this package beside internal/postgres and
// internal/queryproxy against one database, and those packages do commit game
// rows of their own and then change them — legitimately, because they created
// them. A row that appeared in the same instant this snapshot was taken could
// be one of theirs, and calling that damage would be a false alarm on a run
// that did nothing wrong. Ten seconds is far longer than that overlap and far
// shorter than the age of anything a developer would recognise as their own
// data.
const settleFor = 10 * time.Second

// gameRowStatuses is the status of every game database row that was already
// settled in the installation when this package started: instances by
// db_name, templates by template_db.
//
// It is deliberately a whole-installation read. The damage this guards
// against was invisible precisely because each test only ever asked about its
// own rows (outcomeOf, entriesFor, stuckEntryFor) — an honest habit that says
// nothing about the eleven rows the same pass marked 'dropped' on the way
// past.
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

// statusesChangedSince names every row of before whose status has moved.
//
// A row that has since disappeared is not reported: another package's fixture
// deleting its own contest is ordinary cleanup, and the failure this exists to
// catch is a status rewritten in place — the one that strands a database on
// the cluster with nothing left to reclaim it.
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

	// Creating is what makes the bounded worker pool observable: without a
	// high-water mark, "at most three at once" is a claim nothing checks.
	creating, peak int
	slow           time.Duration

	// busy and failIdleDrop are what the reclaim tests use to control
	// DropIdle per database: a name in busy comes back "not dropped, no
	// error" — PostgreSQL refusing a plain DROP DATABASE against a live
	// connection — and a name in failIdleDrop comes back a real error, the
	// two outcomes Service.Reclaim has to tell apart.
	busy         map[string]bool
	failIdleDrop map[string]error
	idleDropped  []string

	// sized, sizeCalls and sizesFail are how the organizer's database list is
	// observed: which names the cluster was asked to measure, in how many
	// calls, and what happens to the list when measuring fails outright.
	sized     []string
	sizeCalls int
	sizesFail error

	// dropFail makes the forcing Drop refuse, which is how the organizer's
	// own drop is checked for the order it does things in: the row must not
	// be marked dropped over a database that is still on the cluster.
	dropFail error

	// idleCalls records every DropIdle call and what it returned, busy and
	// failed ones included — unlike idleDropped, which only ever grows on a
	// success. Reclaim is installation-wide (its own doc), so one test's
	// Reclaim call also processes whatever real reclaimable rows the
	// development database already holds — withRollback keeps the writes off
	// them, not the pass away from them — and an aggregate count off
	// ReclaimResult would then be a claim about that installation rather than
	// about this test's own row. outcomeOf below is how a test asks what
	// happened to its own database specifically, regardless of what else this
	// pass swept.
	idleCalls []idleCall

	// templateBytes, clusterBytes and clusterBytesFail stage the two
	// measurements the pool's byte budget is decided on: how large one copy
	// is, and how much the cluster already holds. clusterReads counts the
	// calls, because "the budget was consulted at all" is a separate claim
	// from "it produced the right number".
	templateBytes    int64
	clusterBytes     int64
	clusterBytesFail error
	clusterReads     int
	// templateReads counts DatabaseSize calls. `pg_database_size` walks the
	// database's own directory, so how often it is asked is the whole of
	// finding 2 — a test that only checked the number it returned would pass
	// just as happily against a version asking once per participant request.
	templateReads int
}

// idleCall is one DropIdle invocation and what the fake told the caller.
type idleCall struct {
	name    string
	dropped bool
	err     error
}

func (c *cluster) CreateInstance(_ context.Context, _, instance string, _ sqlpolicy.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fail != nil {
		return c.fail
	}

	c.creating++
	if c.creating > c.peak {
		c.peak = c.creating
	}
	slow := c.slow
	c.mu.Unlock()

	// Held open so that concurrent creations overlap; a copy that returned
	// instantly would never show the pool being bounded.
	time.Sleep(slow)

	c.mu.Lock()
	c.creating--
	c.made = append(c.made, instance)
	return nil
}

func (c *cluster) highWater() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func (c *cluster) DatabaseSize(context.Context, string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateReads++
	if c.templateBytes > 0 {
		return c.templateBytes, nil
	}
	// A megabyte, so the quota arithmetic has something to multiply.
	return 1 << 20, nil
}

// templateSizeReads is how many times the cluster was asked how large the
// template is.
func (c *cluster) templateSizeReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.templateReads
}

// clusterByteReads is how many times the cluster was asked how full it is —
// zero being the claim a test makes about a deployment with no byte budget,
// which must pay for no measurement at all.
func (c *cluster) clusterByteReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusterReads
}

// setTemplateBytes is a rebuild changing how large one copy costs.
func (c *cluster) setTemplateBytes(size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateBytes = size
}

// fillTo is the cluster filling up between two calls.
func (c *cluster) fillTo(used int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterBytes = used
}

// ClusterBytes is how full the fake cluster is. Zero unless a test says
// otherwise, which is an empty cluster and the case where no byte budget can
// bind.
func (c *cluster) ClusterBytes(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterReads++
	if c.clusterBytesFail != nil {
		return 0, c.clusterBytesFail
	}
	return c.clusterBytes, nil
}

// DatabaseSizes answers a megabyte for every name it is asked about, unless a
// test has set sizesFail — the case Service.Instances has to survive without
// refusing the list, since the rows come from the core database and the sizes
// come from a second system that can be down while it is fine.
func (c *cluster) DatabaseSizes(_ context.Context, names []string) (map[string]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sized = append(c.sized, names...)
	c.sizeCalls++
	if c.sizesFail != nil {
		return nil, c.sizesFail
	}
	sizes := make(map[string]int64, len(names))
	for _, name := range names {
		sizes[name] = 1 << 20
	}
	return sizes, nil
}

// sizeReads is every name DatabaseSizes was asked about, and how many calls it
// took — the second is what proves the list costs one round trip rather than
// one per database.
func (c *cluster) sizeReads() (names []string, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sized...), c.sizeCalls
}

func (c *cluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dropFail != nil {
		return c.dropFail
	}
	c.dropped = append(c.dropped, name)
	return nil
}

// droppedByForce reports whether Drop — the forcing one, not DropIdle — was
// asked to remove name. `dropped` also collects what Invalidate and
// CreateInstance's own pre-drop did, so a test that cares which of the two
// paths removed a database asks by name rather than by count.
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

// DropIdle reports the outcome a test set up for name: busy (not dropped, no
// error), a configured failure, or an ordinary successful drop — recorded in
// idleDropped, kept apart from dropped above so a test can tell Reclaim's own
// drops from whatever Invalidate or TopUp did in the same run.
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

// outcomeOf returns what DropIdle told the caller the last time it was asked
// about name, and whether it was ever asked about it at all — the answer a
// test needs about its own database, independent of every other candidate
// the same installation-wide pass also happened to process.
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

// errRollback ends a test transaction — withRollback's own signal, borrowed
// from internal/postgres/support_test.go, which ends every one of its own
// database tests the same way.
var errRollback = errors.New("rolling back the test transaction")

// withRollback runs body inside a core-database transaction that is always
// rolled back, and hands body that transaction's context.
//
// This is the reclaim tests' isolation, and it is not a tidiness measure.
// Service.Reclaim is installation-wide by design (its own doc): one call
// sweeps every reclaimable row the database holds, not only the rows the
// calling test made. Against a developer's own development database — which
// is what `make test-db` points these tests at — that swept their contests
// too: the fake cluster only pretended to drop the databases, but the rows
// were really marked 'dropped', and Reclaimable skips a dropped row for ever
// after, so the real databases behind them could never be reclaimed again.
// Eleven of them are on the development game cluster right now for exactly
// that reason.
//
// Every repository call the service makes picks this transaction up from the
// context through storage.QuerierFrom, which is the path a real request takes
// too, and markReclaimed's own uow.Do joins it rather than opening a second
// one (storage.PgxUnitOfWork.Do). So the sweep still sees, and still
// processes, exactly what it would in production — CLAUDE.md rule 10's point,
// that the guarantee is proved on the path the deployment uses — while
// nothing it writes outlives the test, whether the row belonged to the test
// or to the developer.
//
// The two alternatives, and why not:
//
//   - A contest id passed to Reclaim, so a test could scope the sweep. An
//     argument only tests pass is a code path only tests exercise: the
//     installation-wide pass, the one production actually runs, would become
//     the untested one — rule 10 again, from the other side.
//   - Asserting only about rows the test created, which is what outcomeOf
//     (above) already does. That keeps the assertions honest and does nothing
//     at all about the damage, because the damage is to the database rather
//     than to the assertion.
//
// TestReclaimLeavesTheInstallationsOwnRowsUntouched (reclaim_test.go) is what
// holds this to its word.
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
// again when the test ends.
//
// Everything it writes goes through storage.QuerierFrom(ctx, testPool) rather
// than straight to the pool, so a caller inside withRollback gets its fixture
// on the same transaction as the code under test — and a caller outside one
// gets the pool, exactly as before.
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
	// Only ever needed by a caller outside a transaction: inside withRollback
	// the rollback has already removed both by the time this runs, and the
	// deletes find nothing.
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

// markTemplateReady stores a 'ready' template database for contest, directly
// against the real schema — the same row gamedb.Provisioner.BuildTemplate
// would leave behind, which nothing in provisioning.Repository creates for a
// test to reuse (BuildTemplate itself belongs to internal/gamedb, one layer
// below this package). Cleanup is contestFor's own: game_templates.contest_id
// cascades on the contest's own delete (migration 3).
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

// The fakes every test in this package shares — the repository, the cluster,
// and the small assembler that puts a *provisioning.Games together out of
// them.
//
// Here rather than in template_test.go, where they used to live: they belong
// to no one source file, and half of what was at the top of that file
// (BeginTableData and everything under it) is tabledata.go's storage rather
// than template.go's. A shared fake in a test file named after one source
// file is a fake nobody looking at any other source file finds (CLAUDE.md
// Go layout rule 5).

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
	// uploads is every upload this fake has ever been told about, by id —
	// enough to back the TemplateRepository methods migration 24 added
	// without this file growing a second kind of fake for them.
	uploads map[uuid.UUID]provisioning.Upload
	// tableData is the table builder's own per-table files, migration 27's
	// counterpart to uploads above.
	tableData map[uuid.UUID]provisioning.TableData
	// deleteRowErr, when set, is what DeleteTableDataRow answers instead of
	// tombstoning. This fake does not reimplement migration 27's own CHECK on
	// deleted_rows — a bound reinvented in Go proves itself and nothing else
	// (postgres.GameInstances.DeleteTableDataRow is where the real one is
	// proved) — so a service test that needs to see the database refuse says
	// so here instead.
	deleteRowErr error
	// tableDataReads, when armed, holds every caller of ReadyTableData until
	// as many of them have arrived as gateTableDataReads was told to expect —
	// two browser tabs pressing "add row" on the same table at the same
	// moment, made deterministic rather than left to the scheduler.
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

// gateTableDataReads arms the gate above for the next n reads of a table's
// current data.
func (s *templateStore) gateTableDataReads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tableDataReads = &arrivalGate{remaining: n, open: make(chan struct{})}
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

// TemplateStatus answers what the real repository's status query does: the
// same row with the script's length in place of the script, and no
// definition — so a test asserting that the status endpoint never reads the
// content is asserting against the same shape production produces.
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
	if s.template.DataChangedAt != nil && !s.template.DataChangedAt.After(claimedAt) {
		s.template.DataChangedAt = nil
	}
	return nil
}

func (s *templateStore) MarkTableDataChanged(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now()
	s.template.DataChangedAt = &at
	return nil
}

func (s *templateStore) RequestBuild(_ context.Context, _ uuid.UUID) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if s.template.Status != provisioning.TemplateReady && s.template.Status != provisioning.TemplateFailed {
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

func (s *templateStore) BeginUpload(_ context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return provisioning.Upload{}, provisioning.ErrUploadInProgress
		}
	}
	if s.uploads == nil {
		s.uploads = map[uuid.UUID]provisioning.Upload{}
	}
	u := provisioning.Upload{
		ID: id, ContestID: contestID, Filename: filename, DeclaredBytes: declaredBytes,
		Status: provisioning.UploadReceiving,
	}
	s.uploads[id] = u
	return u, nil
}

func (s *templateStore) Upload(_ context.Context, id uuid.UUID) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.Upload{}, provisioning.ErrUploadNotFound
	}
	return u, nil
}

func (s *templateStore) CurrentUpload(_ context.Context, contestID uuid.UUID) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return u, nil
		}
	}
	return provisioning.Upload{}, provisioning.ErrUploadNotFound
}

func (s *templateStore) UpdateReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.ErrUploadNotFound
	}
	u.ReceivedBytes = receivedBytes
	s.uploads[id] = u
	return nil
}

func (s *templateStore) CompleteUpload(
	_ context.Context, contestID, id uuid.UUID, database string, summary provisioning.UploadSummary, previous *uuid.UUID,
) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.Template{}, provisioning.ErrUploadNotFound
	}
	u.Status, u.SHA256, u.Lines, u.ReceivedBytes = provisioning.UploadComplete, summary.SHA256, summary.Lines, summary.Bytes
	s.uploads[id] = u
	if previous != nil {
		if p, ok := s.uploads[*previous]; ok {
			p.Status = provisioning.UploadAborted
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

func (s *templateStore) AbortUpload(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.ErrUploadNotFound
	}
	u.Status = provisioning.UploadAborted
	s.uploads[id] = u
	return nil
}

func (s *templateStore) AbandonedUploads(_ context.Context, cutoff time.Time, limit int) ([]provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provisioning.Upload
	for _, u := range s.uploads {
		if u.Status == provisioning.UploadReceiving && u.UpdatedAt.Before(cutoff) {
			out = append(out, u)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *templateStore) UploadInUse(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return false, nil
	}
	if u.Status == provisioning.UploadReceiving {
		return true, nil
	}
	return s.present && s.template.UploadID != nil && *s.template.UploadID == id, nil
}

func (s *templateStore) BeginTableData(_ context.Context, id, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataReceiving {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
	}
	if s.tableData == nil {
		s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	// updated_at is stamped here for the same reason migration 27 gives the
	// column a `DEFAULT now()`: the janitor's cutoff is read off it, and a
	// zero time would make every row this fake ever held look abandoned —
	// a sweep test against that would pass without a cutoff existing at all.
	d := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: declaredBytes,
		Status: provisioning.TableDataReceiving, UpdatedAt: time.Now(),
	}
	s.tableData[id] = d
	return d, nil
}

// ageTableData moves one table-data row's updated_at back, so a janitor test
// can reach a cutoff without waiting for one — the same convention
// upload_test.go's own sweep test uses against the real column.
func (s *templateStore) ageTableData(id uuid.UUID, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.tableData[id]
	d.UpdatedAt = time.Now().Add(-by)
	s.tableData[id] = d
}

func (s *templateStore) TableDataByID(_ context.Context, id uuid.UUID) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	return d, nil
}

func (s *templateStore) CurrentTableData(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataReceiving {
			return d, nil
		}
	}
	return provisioning.TableData{}, provisioning.ErrTableDataNotFound
}

func (s *templateStore) ReadyTableData(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	s.mu.Lock()
	gate := s.tableDataReads
	found, err := provisioning.TableData{}, error(provisioning.ErrTableDataNotFound)
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataComplete {
			found, err = d, nil
			break
		}
	}
	s.mu.Unlock()

	// Released outside the lock, and after the answer has been taken, so that
	// every held caller leaves with the same snapshot — which is what two
	// browser tabs pressing "add row" together actually have.
	if gate != nil {
		gate.arrive()
	}
	return found, err
}

func (s *templateStore) UpdateTableDataReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	d.ReceivedBytes = receivedBytes
	s.tableData[id] = d
	return nil
}

func (s *templateStore) CompleteTableData(
	_ context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID,
) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	d.Status, d.ReceivedBytes, d.Lines = provisioning.TableDataComplete, receivedBytes, lines
	s.tableData[id] = d
	if previous != nil {
		if p, ok := s.tableData[*previous]; ok {
			p.Status = provisioning.TableDataAborted
			s.tableData[*previous] = p
		}
	}
	return d, nil
}

func (s *templateStore) CreateReadyTableData(
	_ context.Context, id, contestID uuid.UUID, table string, bytes, lines int64,
) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataComplete {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
	}
	if s.tableData == nil {
		s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	d := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: bytes, ReceivedBytes: bytes, Lines: lines,
		Status: provisioning.TableDataComplete,
	}
	s.tableData[id] = d
	return d, nil
}

func (s *templateStore) AppendTableDataRow(_ context.Context, id uuid.UUID, receivedBytes, lines int64) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok || d.Status != provisioning.TableDataComplete {
		return provisioning.TableData{}, provisioning.ErrTableDataChanged
	}
	// GREATEST, the same as the real statement's own (postgres.GameInstances.
	// AppendTableDataRow): neither figure ever goes backwards.
	d.ReceivedBytes, d.Lines = max(d.ReceivedBytes, receivedBytes), max(d.Lines, lines)
	s.tableData[id] = d
	return d, nil
}

func (s *templateStore) AbortTableData(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	d.Status = provisioning.TableDataAborted
	s.tableData[id] = d
	return nil
}

func (s *templateStore) DiscardTableData(_ context.Context, contestID uuid.UUID) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uuid.UUID
	for id, d := range s.tableData {
		if d.ContestID != contestID {
			continue
		}
		if d.Status != provisioning.TableDataReceiving && d.Status != provisioning.TableDataComplete {
			continue
		}
		d.Status = provisioning.TableDataAborted
		s.tableData[id] = d
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *templateStore) DeleteTableDataRow(_ context.Context, id uuid.UUID, row int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	if s.deleteRowErr != nil {
		return s.deleteRowErr
	}
	for _, r := range d.DeletedRows {
		if r == row {
			return provisioning.ErrTableRowAlreadyDeleted
		}
	}
	d.DeletedRows = append(append([]int64{}, d.DeletedRows...), row)
	s.tableData[id] = d
	return nil
}

func (s *templateStore) AbandonedTableData(_ context.Context, cutoff time.Time, limit int) ([]provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provisioning.TableData
	for _, d := range s.tableData {
		if d.Status == provisioning.TableDataReceiving && d.UpdatedAt.Before(cutoff) {
			out = append(out, d)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *templateStore) TableDataInUse(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return false, nil
	}
	return d.Status == provisioning.TableDataReceiving || d.Status == provisioning.TableDataComplete, nil
}

// buildCluster records what it was asked to build and can be told to refuse.
type buildCluster struct {
	mu      sync.Mutex
	names   []string
	scripts []string
	policy  sqlpolicy.Policy
	fail    error
	// tableData is every LoadTableData call this fake received, keyed
	// "database.table", and tableDataFail — when set — is what LoadTableData
	// answers instead of loading anything, for the tests that check a
	// data-load failure tears the template down the same way a script
	// failure does.
	tableData     map[string]string
	tableDataFail error
	dropped       []string
}

func (c *buildCluster) BuildTemplate(_ context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error {
	// Read in full before recording: TemplateCluster's real implementation
	// (gamedb.Provisioner.BuildTemplate) streams script rather than holding
	// it all in memory, but what this fake asserts on is the bytes that
	// reached it — an editor's script wrapped in strings.NewReader, or an
	// uploaded file's own contents — so it has to consume the reader the
	// same way a real build would.
	data, err := io.ReadAll(script)
	if err != nil {
		return fmt.Errorf("buildCluster: read script: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.names, c.scripts, c.policy = append(c.names, name), append(c.scripts, string(data)), policy
	return c.fail
}

// LoadTableData records what it was asked to load — the same "read in full"
// reasoning BuildTemplate's own fake gives, since a filtered reader
// (provisioning's own tableDataCopyReader) is exactly what this fake has to
// consume to see what actually reached it.
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

func (c *buildCluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped = append(c.dropped, name)
	return nil
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
