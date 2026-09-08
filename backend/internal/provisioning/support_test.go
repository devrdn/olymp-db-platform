package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
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

func TestMain(m *testing.M) {
	dsn := os.Getenv("CORE_DB_DSN")
	if dsn == "" {
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open the core database: %v\n", err)
		os.Exit(1)
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach the core database: %v\n", err)
		os.Exit(1)
	}
	testPool = pool

	// The standing net under every test in this package, not only the reclaim
	// ones: whatever a test does, no game database row that was already in the
	// developer's installation may have a different status when the package is
	// done. See gameRowStatuses below.
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
	// A megabyte, so the quota arithmetic has something to multiply.
	return 1 << 20, nil
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
