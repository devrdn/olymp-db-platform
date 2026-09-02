package postgres

import (
	"context"
	"errors"
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
			`INSERT INTO contests (created_by, status, sql_mode, writable_tables)
			 VALUES ($1, $2, 'read_write', ARRAY['evidence']::plain_identifier[]) RETURNING id`,
			author.ID, s.contest).Scan(&id); err != nil {
			t.Fatalf("create contest: %v", err)
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
