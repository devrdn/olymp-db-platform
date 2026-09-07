package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// contestWithSpares sets up a contest, some registrations and a pool. It runs
// outside a test transaction because the point of most of these is what
// happens between connections, which one transaction cannot show.
func contestWithSpares(t *testing.T, spares, registrations int) (uuid.UUID, []uuid.UUID) {
	t.Helper()

	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}

	ctx := t.Context()
	author := makeUser(t, ctx, "prov-"+uuid.NewString()[:8])
	contest := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		// The contest cascades to registrations and instances.
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
	})

	repo := NewGameInstances(testPool)
	for i := range spares {
		if err := repo.AddSpare(ctx, contest, "pool_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding spare %d: %v", i, err)
		}
	}

	var people []uuid.UUID
	for i := range registrations {
		user := makeUser(t, ctx, "player-"+uuid.NewString()[:8])
		people = append(people, makeRegistration(t, ctx, contest, user.ID))
		_ = i
	}
	return contest, people
}

// Two late registrations reaching for the last copy is the case the pool
// exists for, and the one a naive claim gets wrong: read a free row, then
// update it, and both take the same database.
//
// Twenty claimants, ten copies. Exactly ten must win, each with a different
// database, and the other ten must be told there is none — not blocked, not
// given a duplicate.
func TestClaimingSpareCopiesUnderContention(t *testing.T) {
	contest, people := contestWithSpares(t, 10, 20)
	repo := NewGameInstances(testPool)

	type outcome struct {
		database string
		err      error
	}
	results := make(chan outcome, len(people))

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	for _, registration := range people {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // all twenty go at once
			database, err := repo.ClaimSpare(context.Background(), contest, registration, 1)
			results <- outcome{database, err}
		}()
	}
	start.Done()
	done.Wait()
	close(results)

	taken := map[string]int{}
	empty := 0
	for r := range results {
		switch {
		case r.err == nil:
			taken[r.database]++
		case errors.Is(r.err, provisioning.ErrNoSpare):
			empty++
		default:
			t.Fatalf("claiming failed for a reason that is not an empty pool: %v", r.err)
		}
	}

	if len(taken) != 10 || empty != 10 {
		t.Fatalf("%d copies taken by %d claimants, %d told the pool was empty", len(taken), len(taken), empty)
	}
	for database, times := range taken {
		if times != 1 {
			t.Fatalf("%s was handed to %d participants", database, times)
		}
	}
}

// A copy of an older template must not be handed out: it holds the previous
// game's data and the previous policy's grants.
func TestAStaleCopyIsNeverClaimed(t *testing.T) {
	contest, people := contestWithSpares(t, 3, 1)
	repo := NewGameInstances(testPool)

	if _, err := testPool.Exec(t.Context(),
		`UPDATE game_instances SET template_version = 1 WHERE contest_id = $1`, contest); err != nil {
		t.Fatalf("ageing the pool: %v", err)
	}

	_, err := repo.ClaimSpare(t.Context(), contest, people[0], 2)
	if !errors.Is(err, provisioning.ErrNoSpare) {
		t.Fatalf("error = %v, want ErrNoSpare; a stale copy was on offer", err)
	}
}

func TestThePoolDepthAndTheStartGate(t *testing.T) {
	contest, people := contestWithSpares(t, 4, 1)
	repo := NewGameInstances(testPool)

	if depth, err := repo.SpareCount(t.Context(), contest, 1); err != nil || depth != 4 {
		t.Fatalf("depth = %d (%v), want 4", depth, err)
	}

	// Claiming one takes it out of the pool without removing it from the
	// contest: the depth falls, the gate is unaffected.
	if _, err := repo.ClaimSpare(t.Context(), contest, people[0], 1); err != nil {
		t.Fatalf("claiming: %v", err)
	}
	if depth, err := repo.SpareCount(t.Context(), contest, 1); err != nil || depth != 3 {
		t.Fatalf("depth = %d (%v), want 3", depth, err)
	}

	if ready, err := repo.AllCurrent(t.Context(), contest, 1); err != nil || !ready {
		t.Fatalf("ready = %v (%v), want true", ready, err)
	}
	// A rebuild leaves every one of them behind, and the contest may not start.
	if ready, err := repo.AllCurrent(t.Context(), contest, 2); err != nil || ready {
		t.Fatalf("ready = %v (%v) after a rebuild, want false", ready, err)
	}
}

// Both kinds of leftover have to be found: the claimed ones because somebody
// would play on them, and the free ones because whoever registers next would
// be handed one.
func TestStaleFindsClaimedAndFreeAlike(t *testing.T) {
	contest, people := contestWithSpares(t, 3, 1)
	repo := NewGameInstances(testPool)

	if _, err := repo.ClaimSpare(t.Context(), contest, people[0], 1); err != nil {
		t.Fatalf("claiming: %v", err)
	}

	stale, err := repo.Stale(t.Context(), contest, 2)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(stale) != 3 {
		t.Fatalf("found %d stale databases, want all 3", len(stale))
	}

	var owned, free int
	for _, s := range stale {
		if s.Registration == nil {
			free++
		} else {
			owned++
		}
	}
	if owned != 1 || free != 2 {
		t.Fatalf("owned = %d, free = %d; want 1 and 2", owned, free)
	}
}

// The composite reference is what makes it impossible to hand one contest's
// spare copy to somebody registered for another. A check in the application
// would be a check somebody eventually forgets.
func TestACopyCannotBeGivenToAnotherContestsParticipant(t *testing.T) {
	first, _ := contestWithSpares(t, 1, 0)
	_, elsewhere := contestWithSpares(t, 0, 1)

	repo := NewGameInstances(testPool)
	_, err := repo.ClaimSpare(t.Context(), first, elsewhere[0], 1)
	if err == nil {
		t.Fatal("a copy was handed to a participant of another contest")
	}
	if errors.Is(err, provisioning.ErrNoSpare) {
		t.Fatal("refused as an empty pool; the database should have refused the reference itself")
	}
}

func TestOfReportsWhenThereIsNothingYet(t *testing.T) {
	_, people := contestWithSpares(t, 0, 1)

	if _, err := NewGameInstances(testPool).Of(t.Context(), people[0]); !errors.Is(err, provisioning.ErrNoInstance) {
		t.Fatalf("error = %v, want ErrNoInstance", err)
	}
}

var _ = storage.QuerierFrom

// The background job asks this for its work list, so what it leaves out
// matters as much as what it returns: a draft has nobody to provision for, and
// a template still building is not a contest to copy.
func TestLiveListsOnlyContestsWorthProvisioningFor(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	ctx := t.Context()
	repo := NewGameInstances(testPool)

	author := makeUser(t, ctx, "live-"+uuid.NewString()[:8])
	type setup struct{ contest, template string }
	made := map[uuid.UUID]setup{}

	for _, s := range []setup{
		{"published", "ready"},
		{"running", "ready"},
		{"draft", "ready"},
		{"published", "building"},
		{"finished", "ready"},
	} {
		var id uuid.UUID
		if err := testPool.QueryRow(ctx,
			`INSERT INTO contests (created_by, status) VALUES ($1, $2) RETURNING id`,
			author.ID, s.contest).Scan(&id); err != nil {
			t.Fatalf("create contest: %v", err)
		}
		if _, err := testPool.Exec(ctx,
			`INSERT INTO contest_sql_policies (contest_id, mode, writable_tables)
			 VALUES ($1, 'read_write', ARRAY['evidence']::plain_table_name[])`, id); err != nil {
			t.Fatalf("create policy: %v", err)
		}
		if _, err := testPool.Exec(ctx,
			`INSERT INTO game_templates (contest_id, template_db, init_script, status, version)
			 VALUES ($1, $2, 'SELECT 1', $3, 3)`,
			id, "tpl_"+uuid.NewString()[:10], s.template); err != nil {
			t.Fatalf("create template: %v", err)
		}
		made[id] = s
		t.Cleanup(func() {
			clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
		})
	}

	live, err := repo.Live(ctx)
	if err != nil {
		t.Fatalf("live: %v", err)
	}

	found := map[uuid.UUID]bool{}
	for _, c := range live {
		if _, ours := made[c.ID]; ours {
			found[c.ID] = true
			// The policy has to arrive whole, or a pool is built with the
			// wrong grants — the failure nobody sees until a participant
			// writes to a table they should not have reached.
			if c.Policy.Mode != sqlpolicy.ModeReadWrite || len(c.Policy.WritableTables) != 1 {
				t.Fatalf("policy arrived as %+v", c.Policy)
			}
			if err := c.Policy.Validate(); err != nil {
				t.Fatalf("a stored policy does not validate: %v", err)
			}
			if c.Version != 3 {
				t.Fatalf("version = %d, want the template's 3", c.Version)
			}
		}
	}

	for id, s := range made {
		want := (s.contest == "published" || s.contest == "running") && s.template == "ready"
		if found[id] != want {
			t.Fatalf("contest %s/%s: listed = %v, want %v", s.contest, s.template, found[id], want)
		}
	}
}

// reclaimContest creates a contest already at status, with its updated_at
// backdated by age. Reclaimable judges a contest's finish moment by that
// column — SetStatus and AdvanceFinished (contests.go) both set it in the
// same statement that moves status to finished — never by a moment computed
// in this process, so backdating it here is what stands in for "finished
// this long ago" without waiting for real time to pass. graceMin, when not
// nil, becomes the contest's own settings.grace_period_min.
//
// Every Reclaimable test below calls this from inside withTx, on purpose:
// Reclaim is installation-wide (its own doc), and internal/provisioning's
// own reclaim_test.go really drops whatever the shared database offers it,
// on a schedule this package does not control. `go test ./...` runs that
// package concurrently with this one against the same PostgreSQL, so a row
// committed here would be a real, live candidate for somebody else's Reclaim
// call before this test ever got to read it back — not merely crowded out
// of a batch limit's window, but genuinely gone. Writing through
// storage.QuerierFrom(ctx, testPool) keeps the insert on the caller's own
// transaction, invisible to any other session until it commits, which it
// never does — withTx always rolls back. That is the same lever
// audit_test.go's writeTrail reaches for (scoping, not a wider limit or a
// different pass order), applied one level earlier because here the shared
// state a concurrent process could touch is mutable, not merely readable.
func reclaimContest(t *testing.T, ctx context.Context, status string, age time.Duration, graceMin *int) uuid.UUID {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	author := makeUser(t, ctx, "reclaim-"+uuid.NewString()[:8])

	settings := "{}"
	if graceMin != nil {
		settings = fmt.Sprintf(`{"grace_period_min": %d}`, *graceMin)
	}

	var id uuid.UUID
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`INSERT INTO contests (created_by, status, settings, updated_at)
		 VALUES ($1, $2, $3::jsonb, now() - make_interval(mins => $4::int))
		 RETURNING id`, author.ID, status, settings, int(age.Minutes())).Scan(&id); err != nil {
		t.Fatalf("create contest: %v", err)
	}
	return id
}

// reclaimableContestIDs is the set of contest ids Reclaimable actually
// offered, so a test can ask "was mine in there" rather than trust the
// result's raw length or order.
func reclaimableContestIDs(candidates []provisioning.ReclaimCandidate) map[uuid.UUID]bool {
	found := make(map[uuid.UUID]bool, len(candidates))
	for _, c := range candidates {
		found[c.ContestID] = true
	}
	return found
}

// A contest finished well past the installation's own default grace, with no
// grace of its own configured, is exactly the ordinary case the sweep exists
// for.
func TestReclaimableFindsAFinishedContestPastItsGrace(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		repo := NewGameInstances(testPool)
		if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		if !reclaimableContestIDs(candidates)[contest] {
			t.Fatal("a finished contest past its grace was not offered for reclaim")
		}
	})
}

// The grace exists precisely to protect this: a contest finished a moment ago
// must not be touched yet.
func TestReclaimableSkipsAContestStillWithinGrace(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 5*time.Minute, nil)
		repo := NewGameInstances(testPool)
		if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		if reclaimableContestIDs(candidates)[contest] {
			t.Fatal("a contest still inside its grace was offered for reclaim")
		}
	})
}

// Archiving is the ordinary "put this away" action an organizer reaches for
// once a contest is done — contests.allowedTransitions lets a finished
// contest move straight to archived — and it must not exempt the contest's
// instances from the sweep forever. That was the unbounded leak this widened
// status filter exists to close.
func TestReclaimableIncludesAnArchivedContestPastItsGrace(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "archived", 2*time.Hour, nil)
		repo := NewGameInstances(testPool)
		if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		if !reclaimableContestIDs(candidates)[contest] {
			t.Fatal("an archived contest past its grace was not offered for reclaim")
		}
	})
}

// limit is what stops a fresh deployment's first tick from asking the
// cluster to drop every historical instance in one call — the deploy-day
// hazard provisioning.ReclaimBatchLimit's own doc explains. Two candidates of
// its own guarantee at least two rows exist inside this test's own
// transaction, regardless of anything else in the shared database, so a
// limit of one must return exactly one either way.
func TestReclaimableLimitCapsHowManyInstancesOneCallReturns(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)
		for range 2 {
			contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
			if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
				t.Fatalf("adding an instance: %v", err)
			}
		}

		candidates, err := repo.Reclaimable(ctx, 60, 1)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		if len(candidates) != 1 {
			t.Fatalf("got %d candidates with limit 1, want exactly 1", len(candidates))
		}
	})
}

// A running or published contest is never a candidate, however old its
// updated_at happens to be — that column moves for reasons other than
// finishing (an organizer editing settings, §4.1), and none of them say the
// contest is over.
func TestReclaimableNeverTouchesARunningOrPublishedContest(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)

		for _, status := range []string{"running", "published", "draft"} {
			contest := reclaimContest(t, ctx, status, 30*24*time.Hour, nil)
			if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
				t.Fatalf("adding an instance to a %s contest: %v", status, err)
			}

			candidates, err := repo.Reclaimable(ctx, 1, provisioning.ReclaimBatchLimit)
			if err != nil {
				t.Fatalf("reclaimable: %v", err)
			}
			if reclaimableContestIDs(candidates)[contest] {
				t.Fatalf("a %s contest was offered for reclaim", status)
			}
		}
	})
}

// A contest's own grace_period_min is what actually governs it — the
// installation's own figure is only what an unconfigured contest defers to.
func TestReclaimableHonoursTheContestsOwnGracePeriod(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)

		short := 5
		// Finished 10 minutes ago; its own 5-minute grace has passed although
		// the installation default below would not have let it through on
		// its own.
		tighter := reclaimContest(t, ctx, "finished", 10*time.Minute, &short)
		if err := repo.AddSpare(ctx, tighter, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		long := 1000
		// Finished 10 minutes ago too, but its own grace is nowhere near
		// over, although the installation default alone would have let it
		// through.
		looser := reclaimContest(t, ctx, "finished", 10*time.Minute, &long)
		if err := repo.AddSpare(ctx, looser, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		found := reclaimableContestIDs(candidates)
		if !found[tighter] {
			t.Fatal("a contest whose own tighter grace had passed was not offered")
		}
		if found[looser] {
			t.Fatal("a contest whose own longer grace had not passed was offered anyway")
		}
	})
}

// An instance already marked dropped is history, not work: offering it again
// would have the sweep asking the cluster to drop something twice.
func TestReclaimableExcludesInstancesAlreadyDropped(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		repo := NewGameInstances(testPool)
		database := "reclaim_" + uuid.NewString()[:12]
		if err := repo.AddSpare(ctx, contest, database, 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}
		if err := repo.MarkDropped(ctx, database); err != nil {
			t.Fatalf("marking dropped: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		if reclaimableContestIDs(candidates)[contest] {
			t.Fatal("an already-dropped instance was offered for reclaim again")
		}
	})
}

// A tight limit must spend itself on whoever has been overdue the longest,
// not on whichever contest's UUID happens to sort first (finding 2:
// contest_id is UUID order, unrelated to how overdue a candidate is, so a
// large contest that sorts first used to consume every tick's whole batch
// while everyone else waited behind it). Ordering by deadline instead of
// contest_id is what this test proves: the contest finished longer ago comes
// back before the one finished more recently, regardless of which of the two
// randomly generated UUIDs is numerically smaller.
func TestReclaimableOrdersTheOldestDeadlineFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)

		older := reclaimContest(t, ctx, "finished", 4*time.Hour, nil)
		if err := repo.AddSpare(ctx, older, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}
		newer := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		if err := repo.AddSpare(ctx, newer, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		candidates, err := repo.Reclaimable(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable: %v", err)
		}
		olderIdx, newerIdx := -1, -1
		for i, c := range candidates {
			switch c.ContestID {
			case older:
				olderIdx = i
			case newer:
				newerIdx = i
			}
		}
		if olderIdx == -1 || newerIdx == -1 {
			t.Fatalf("both contests must be offered; older found=%v newer found=%v", olderIdx != -1, newerIdx != -1)
		}
		if olderIdx >= newerIdx {
			t.Fatalf("the contest overdue since 4h ago is at index %d, the one overdue since 2h ago is at index %d; "+
				"want the older deadline first regardless of contest_id order", olderIdx, newerIdx)
		}
	})
}

// The row survives, in the terminal status the schema has carried since
// migration 3 for exactly this — an organizer's audit search has to have
// something to find even once the database itself is gone.
func TestMarkDroppedLeavesTheRowBehindAsHistory(t *testing.T) {
	contest, _ := contestWithSpares(t, 1, 0)
	repo := NewGameInstances(testPool)

	spares, err := repo.Stale(t.Context(), contest, 2) // version 2 > 1 catches every copy
	if err != nil || len(spares) != 1 {
		t.Fatalf("setup: listing the spare: %v (%d found)", err, len(spares))
	}
	database := spares[0].Database

	if err := repo.MarkDropped(t.Context(), database); err != nil {
		t.Fatalf("mark dropped: %v", err)
	}

	var status string
	if err := testPool.QueryRow(t.Context(),
		`SELECT status FROM game_instances WHERE db_name = $1`, database).Scan(&status); err != nil {
		t.Fatalf("the row was removed rather than marked: %v", err)
	}
	if status != "dropped" {
		t.Fatalf("status = %q, want %q", status, "dropped")
	}
}

// CLAUDE.md rule 7: the index lands with the query. Reclaimable and
// ReclaimableTemplates both filter game_instances on `status <> 'dropped'`,
// which the plain status index (migration 3) serves poorly once 'dropped'
// becomes the majority of the table — this proves the partial index
// migration 000021 added is actually on the schema the sweep runs against,
// not merely described in a migration file nobody applied yet.
func TestTheActiveInstancesIndexExists(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	var name string
	err := testPool.QueryRow(t.Context(),
		`SELECT indexname FROM pg_indexes WHERE tablename = 'game_instances' AND indexname = 'game_instances_active_idx'`).
		Scan(&name)
	if err != nil {
		t.Fatalf("game_instances_active_idx is missing (run make migrate-up): %v", err)
	}
}

// reclaimTemplate stores a template database for contest in the given
// status, so a ReclaimableTemplates test has something to offer or exclude.
// Like reclaimContest, it writes through the ctx's ambient transaction (or
// the raw pool when there is none) — every call site below passes the ctx
// withTx hands its body, for the same reason reclaimContest's own doc gives.
func reclaimTemplate(t *testing.T, ctx context.Context, contest uuid.UUID, status string) string {
	t.Helper()
	database := "reclaim_tpl_" + uuid.NewString()[:12]
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`INSERT INTO game_templates (contest_id, template_db, init_script, status, version)
		 VALUES ($1, $2, 'SELECT 1', $3, 1)`, contest, database, status); err != nil {
		t.Fatalf("create template: %v", err)
	}
	return database
}

// The largest single database a contest owns is offered once the contest is
// finished past its grace and nothing is left that still needs it.
func TestReclaimableTemplatesOffersATemplateWithNoInstancesLeft(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		database := reclaimTemplate(t, ctx, contest, "ready")
		repo := NewGameInstances(testPool)

		templates, err := repo.ReclaimableTemplates(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable templates: %v", err)
		}
		found := false
		for _, tpl := range templates {
			if tpl.ContestID == contest {
				found = true
				if tpl.Database != database {
					t.Fatalf("template database = %q, want %q", tpl.Database, database)
				}
			}
		}
		if !found {
			t.Fatal("a template with no instances left was not offered for reclaim")
		}
	})
}

// A template must never be offered while one of its own instances could
// still be recreated from it — dropping the source ahead of what depends on
// it would be exactly the ordering Reclaim's own doc says is backwards.
func TestReclaimableTemplatesExcludesATemplateWithALiveInstanceStillThere(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		reclaimTemplate(t, ctx, contest, "ready")
		repo := NewGameInstances(testPool)
		if err := repo.AddSpare(ctx, contest, "reclaim_"+uuid.NewString()[:12], 1); err != nil {
			t.Fatalf("adding an instance: %v", err)
		}

		templates, err := repo.ReclaimableTemplates(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable templates: %v", err)
		}
		for _, tpl := range templates {
			if tpl.ContestID == contest {
				t.Fatal("a template was offered while a live instance still needed it")
			}
		}
	})
}

// Only a built template ('ready') has a database on disk to remove; an
// unbuilt or already-reclaimed one is nothing the cluster can be asked to
// drop twice.
func TestReclaimableTemplatesExcludesAnUnbuiltOrAlreadyDroppedTemplate(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)
		for _, status := range []string{"pending", "building", "failed", "dropped"} {
			contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
			reclaimTemplate(t, ctx, contest, status)

			templates, err := repo.ReclaimableTemplates(ctx, 60, provisioning.ReclaimBatchLimit)
			if err != nil {
				t.Fatalf("reclaimable templates (%s): %v", status, err)
			}
			for _, tpl := range templates {
				if tpl.ContestID == contest {
					t.Fatalf("a %s template was offered for reclaim", status)
				}
			}
		}
	})
}

// The same fairness Reclaimable's own ordering test proves, for the largest
// database a contest owns: the template belonging to the longer-overdue
// contest comes back first, regardless of contest_id order.
func TestReclaimableTemplatesOrdersTheOldestDeadlineFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewGameInstances(testPool)

		older := reclaimContest(t, ctx, "finished", 4*time.Hour, nil)
		reclaimTemplate(t, ctx, older, "ready")
		newer := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		reclaimTemplate(t, ctx, newer, "ready")

		templates, err := repo.ReclaimableTemplates(ctx, 60, provisioning.ReclaimBatchLimit)
		if err != nil {
			t.Fatalf("reclaimable templates: %v", err)
		}
		olderIdx, newerIdx := -1, -1
		for i, tpl := range templates {
			switch tpl.ContestID {
			case older:
				olderIdx = i
			case newer:
				newerIdx = i
			}
		}
		if olderIdx == -1 || newerIdx == -1 {
			t.Fatalf("both templates must be offered; older found=%v newer found=%v", olderIdx != -1, newerIdx != -1)
		}
		if olderIdx >= newerIdx {
			t.Fatalf("the template overdue since 4h ago is at index %d, the one overdue since 2h ago is at index %d; "+
				"want the older deadline first regardless of contest_id order", olderIdx, newerIdx)
		}
	})
}

// The row survives as history, the same convention MarkDropped keeps for an
// instance — an organizer's audit search has to have something to find even
// once the database itself is gone.
func TestMarkTemplateDroppedLeavesTheRowBehindAsHistory(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		contest := reclaimContest(t, ctx, "finished", 2*time.Hour, nil)
		reclaimTemplate(t, ctx, contest, "ready")
		repo := NewGameInstances(testPool)

		if err := repo.MarkTemplateDropped(ctx, contest); err != nil {
			t.Fatalf("mark template dropped: %v", err)
		}

		var status string
		if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
			`SELECT status FROM game_templates WHERE contest_id = $1`, contest).Scan(&status); err != nil {
			t.Fatalf("the row was removed rather than marked: %v", err)
		}
		if status != "dropped" {
			t.Fatalf("status = %q, want %q", status, "dropped")
		}
	})
}

// The organizer's list of a contest's databases. It has to carry both kinds
// of row — a spare nobody holds and a participant's own copy — and it has to
// name the holder, because "which of these is Ivan's" is the question the
// screen exists to answer.
func TestInstancesListsSparesAndClaimedCopiesAlike(t *testing.T) {
	contest, people := contestWithSpares(t, 2, 1)
	repo := NewGameInstances(testPool)

	claimed, err := repo.ClaimSpare(t.Context(), contest, people[0], 1)
	if err != nil {
		t.Fatalf("setup: claiming a spare: %v", err)
	}

	list, err := repo.Instances(t.Context(), contest, 100)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d rows, want the 2 this contest has", len(list))
	}

	var held, spare int
	for _, row := range list {
		switch {
		case row.Database == claimed:
			held++
			if row.Registration == nil || *row.Registration != people[0] {
				t.Fatalf("the claimed copy came back with registration %v, want %v", row.Registration, people[0])
			}
			if row.ParticipantLogin == "" {
				t.Fatal("the claimed copy names nobody; the screen cannot say whose it is")
			}
			if row.Status != "ready" || row.TemplateVersion != 1 {
				t.Fatalf("the claimed copy is %q at version %d, want ready at 1", row.Status, row.TemplateVersion)
			}
			if row.CreatedAt.IsZero() {
				t.Fatal("the claimed copy carries no creation time")
			}
		default:
			spare++
			if row.Registration != nil {
				t.Fatalf("%s is held by %v but nobody claimed it", row.Database, row.Registration)
			}
			if row.ParticipantLogin != "" {
				t.Fatalf("a spare names %q as its holder", row.ParticipantLogin)
			}
		}
	}
	if held != 1 || spare != 1 {
		t.Fatalf("%d claimed and %d spare, want one of each", held, spare)
	}
}

// A contest-scoped screen must never show another contest's rows: db_name is
// unique installation-wide, so scoping is the query's job and nothing above
// it can add the scope back.
func TestInstancesNeverCrossesIntoAnotherContest(t *testing.T) {
	mine, _ := contestWithSpares(t, 1, 0)
	theirs, _ := contestWithSpares(t, 3, 0)
	repo := NewGameInstances(testPool)

	list, err := repo.Instances(t.Context(), mine, 100)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d rows for a contest with one spare; %s has three", len(list), theirs)
	}
}

// The bound is the caller's, and the query has to honour it — otherwise the
// "there are more than this" the service reports is a guess.
func TestInstancesHonoursTheLimitItIsGiven(t *testing.T) {
	contest, _ := contestWithSpares(t, 4, 0)
	repo := NewGameInstances(testPool)

	list, err := repo.Instances(t.Context(), contest, 2)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d rows for a limit of 2", len(list))
	}
}

// A row already reclaimed still appears: it is what the trail's payload
// points at, and an organizer who cannot find a database wants to see that it
// was there and is gone, not an absence.
func TestInstancesKeepsShowingADroppedRow(t *testing.T) {
	contest, _ := contestWithSpares(t, 1, 0)
	repo := NewGameInstances(testPool)

	first, err := repo.Instances(t.Context(), contest, 100)
	if err != nil || len(first) != 1 {
		t.Fatalf("setup: %v (%d rows)", err, len(first))
	}
	if err := repo.MarkDropped(t.Context(), first[0].Database); err != nil {
		t.Fatalf("mark dropped: %v", err)
	}

	after, err := repo.Instances(t.Context(), contest, 100)
	if err != nil {
		t.Fatalf("Instances: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("listed %d rows; a dropped row is history and must stay visible", len(after))
	}
	if after[0].Status != "dropped" {
		t.Fatalf("status = %q, want dropped", after[0].Status)
	}
}

// The lookup a destructive action is decided on. Scoped to the contest, so
// naming another contest's database is "no such database" rather than a way
// to drop it with a permission over this one.
func TestInstanceNamedIsScopedToItsOwnContest(t *testing.T) {
	mine, _ := contestWithSpares(t, 1, 0)
	theirs, _ := contestWithSpares(t, 1, 0)
	repo := NewGameInstances(testPool)

	ours, err := repo.Instances(t.Context(), mine, 10)
	if err != nil || len(ours) != 1 {
		t.Fatalf("setup: %v (%d rows)", err, len(ours))
	}
	elsewhere, err := repo.Instances(t.Context(), theirs, 10)
	if err != nil || len(elsewhere) != 1 {
		t.Fatalf("setup: %v (%d rows)", err, len(elsewhere))
	}

	found, err := repo.InstanceNamed(t.Context(), mine, ours[0].Database)
	if err != nil {
		t.Fatalf("InstanceNamed on our own database: %v", err)
	}
	if found.Database != ours[0].Database {
		t.Fatalf("found %q, want %q", found.Database, ours[0].Database)
	}

	if _, err := repo.InstanceNamed(t.Context(), mine, elsewhere[0].Database); !errors.Is(err, provisioning.ErrInstanceNotFound) {
		t.Fatalf("looking up another contest's database returned %v, want ErrInstanceNotFound", err)
	}
	if _, err := repo.InstanceNamed(t.Context(), mine, "game_no_such_database"); !errors.Is(err, provisioning.ErrInstanceNotFound) {
		t.Fatalf("looking up a name that does not exist returned %v, want ErrInstanceNotFound", err)
	}
}

// CLAUDE.md rule 7 again, in its weaker form: Instances above does not add a
// predicate, it adds a second reader for one migration 12 already serves —
// and the comment above the query says so. This is what stops that comment
// from becoming a lie: the index has to exist, and contest_id has to be its
// leading column, or `WHERE contest_id = $1` is a sequential scan of a table
// that only ever grows.
func TestTheContestInstancesIndexLeadsWithTheContest(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}

	var definition string
	err := testPool.QueryRow(t.Context(), `
		SELECT indexdef FROM pg_indexes
		WHERE tablename = 'game_instances' AND indexname = 'game_instances_contest_version_idx'`).
		Scan(&definition)
	if err != nil {
		t.Fatalf("game_instances_contest_version_idx is missing (run make migrate-up): %v", err)
	}
	if !strings.Contains(definition, "(contest_id") {
		t.Fatalf("contest_id does not lead the index: %s", definition)
	}
}
