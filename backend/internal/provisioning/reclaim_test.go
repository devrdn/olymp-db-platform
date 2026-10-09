package provisioning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

// finishContest backdates a contest to have finished ageMinutes ago, writing
// the updated_at a real status move would have.
func finishContest(t *testing.T, ctx context.Context, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = 'finished', updated_at = now() - make_interval(mins => $2::int) WHERE id = $1`,
		contest, ageMinutes); err != nil {
		t.Fatalf("finishing the contest: %v", err)
	}
}

// archiveContest is finishContest for the archived status.
func archiveContest(t *testing.T, ctx context.Context, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = 'archived', updated_at = now() - make_interval(mins => $2::int) WHERE id = $1`,
		contest, ageMinutes); err != nil {
		t.Fatalf("archiving the contest: %v", err)
	}
}

// sink is a minimal audit.Sink recording what it was told.
type sink struct{ entries []audit.Entry }

func (s *sink) Append(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

func (s *sink) AppendMany(_ context.Context, entries []audit.Entry) error {
	s.entries = append(s.entries, entries...)
	return nil
}

// serviceWithAudit is serviceFor plus the audit trail. It uses one worker
// because callers run inside withRollback, and a pgx transaction is one
// connection that TopUp's workers must not share across goroutines.
func serviceWithAudit(t *testing.T, ctx context.Context, registrations int) (*provisioning.Service, *cluster, *sink, provisioning.Contest, []uuid.UUID) {
	t.Helper()

	contest, people := contestFor(t, ctx, registrations)
	fake := &cluster{}
	s := &sink{}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).
		WithWorkers(1).
		WithAudit(audit.New(s), storage.NewUnitOfWork(testPool))
	return service, fake, s, contest, people
}

// instancesOf lists every not-yet-dropped database of contest.
func instancesOf(t *testing.T, ctx context.Context, contest uuid.UUID) []provisioning.Stale {
	t.Helper()
	// version 2 catches every copy: TopUp always makes version-1 copies here.
	found, err := postgres.NewGameInstances(testPool).Stale(ctx, contest, 2)
	if err != nil {
		t.Fatalf("listing instances: %v", err)
	}
	return found
}

func statusOf(t *testing.T, ctx context.Context, database string) string {
	t.Helper()
	var status string
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT status FROM game_instances WHERE db_name = $1`, database).Scan(&status); err != nil {
		t.Fatalf("reading %s: %v", database, err)
	}
	return status
}

func templateStatusOf(t *testing.T, ctx context.Context, contest uuid.UUID) string {
	t.Helper()
	var status string
	if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx,
		`SELECT status FROM game_templates WHERE contest_id = $1`, contest).Scan(&status); err != nil {
		t.Fatalf("reading the template of %s: %v", contest, err)
	}
	return status
}

// Reclaim sweeps every due contest in the shared database, not only the
// test's own. withRollback keeps its writes from landing; the helpers below
// scope what a test reads from the result, the fake cluster and the sink to
// its own contest, since those can also carry other committed contests.

func wasDropped(fake *cluster, database string) bool {
	dropped, _, _ := fake.outcomeOf(database)
	return dropped
}

// entriesFor returns the entries s recorded about contest.
func entriesFor(s *sink, contest uuid.UUID) []audit.Entry {
	var found []audit.Entry
	id := contest.String()
	for _, e := range s.entries {
		if e.EntityID == id {
			found = append(found, e)
		}
	}
	return found
}

// stuckEntryFor finds database's entry in result.Stuck, if any.
func stuckEntryFor(result provisioning.ReclaimResult, database string) (provisioning.StuckInstance, bool) {
	for _, s := range result.Stuck {
		if s.Database == database {
			return s, true
		}
	}
	return provisioning.StuckInstance{}, false
}

// The bystander is a committed, real candidate, so the sweep reaches it and
// marks it dropped inside the test's transaction; a second connection must
// still see it 'ready'. The `reached` check keeps the test from passing just
// because Reclaim never saw the row.
func TestReclaimLeavesTheInstallationsOwnRowsUntouched(t *testing.T) {
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	database := bystanderInstance(t)

	var reached bool
	withRollback(t, func(ctx context.Context) {
		service, fake, _, _, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.Reclaim(ctx, 60); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		_, _, reached = fake.outcomeOf(database)
	})

	if !reached {
		t.Fatalf("the sweep never reached %s, so this test proves nothing about the rows it does reach", database)
	}
	if status := committedStatusOf(t, database); status != "ready" {
		t.Fatalf("a committed row this test did not create is %q after the sweep, want %q — "+
			"`make test-db` must not reclaim the developer's own databases", status, "ready")
	}
}

// bystanderInstance commits, outside any test transaction, a finished contest
// past its grace with one 'ready' instance, and removes it on cleanup.
func bystanderInstance(t *testing.T) string {
	t.Helper()
	ctx := t.Context()

	var author uuid.UUID
	if err := testPool.QueryRow(ctx,
		`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Bystander', 'x')
		 RETURNING id`, "prov-bystander-"+uuid.NewString()[:8]).Scan(&author); err != nil {
		t.Fatalf("create the bystander's author: %v", err)
	}

	var contest uuid.UUID
	if err := testPool.QueryRow(ctx,
		`INSERT INTO contests (created_by, status, updated_at)
		 VALUES ($1, 'finished', now() - make_interval(mins => 120))
		 RETURNING id`, author).Scan(&contest); err != nil {
		t.Fatalf("create the bystander contest: %v", err)
	}
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The contest cascades to its instances (migration 3).
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contest)
		_, _ = testPool.Exec(clean, `DELETE FROM users WHERE id = $1`, author)
	})

	database := "game_bystander_" + uuid.NewString()[:12]
	if err := postgres.NewGameInstances(testPool).AddSpare(ctx, contest, database, 1); err != nil {
		t.Fatalf("create the bystander instance: %v", err)
	}
	return database
}

// committedStatusOf reads a row's status on the pool, outside any test
// transaction, as a second connection sees it.
func committedStatusOf(t *testing.T, database string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM game_instances WHERE db_name = $1`, database).Scan(&status); err != nil {
		t.Fatalf("reading %s: %v", database, err)
	}
	return status
}

func TestReclaimDropsAnInstanceOfAContestPastItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, s, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database

		finishContest(t, ctx, contest.ID, 120)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		if !wasDropped(fake, database) {
			t.Fatalf("the cluster was never asked to drop %s; result = %+v", database, result)
		}
		if status := statusOf(t, ctx, database); status != "dropped" {
			t.Fatalf("status = %q, want %q — the row must survive, not be deleted", status, "dropped")
		}

		entries := entriesFor(s, contest.ID)
		if len(entries) != 1 {
			t.Fatalf("%d audit entries were written about this contest, want 1 (found %+v)", len(entries), entries)
		}
		entry := entries[0]
		if entry.Action != audit.ActionGameInstanceReclaim || entry.Entity != "contest" || entry.EntityID != contest.ID.String() {
			t.Fatalf("audit entry = %+v; wrong action or entity", entry)
		}
		if entry.ActorID != nil {
			t.Fatal("a system sweep recorded an actor")
		}
		if entry.Payload["database"] != database {
			t.Fatalf("payload database = %v, want %q — how an organizer finds it", entry.Payload["database"], database)
		}
	})
}

// Archiving a finished contest must not exempt its instances from the sweep.
func TestReclaimDropsAnInstanceOfAnArchivedContestPastItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database

		archiveContest(t, ctx, contest.ID, 120)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		if !wasDropped(fake, database) {
			t.Fatalf("an archived contest past its grace was not dropped; result = %+v", result)
		}
		if status := statusOf(t, ctx, database); status != "dropped" {
			t.Fatalf("status = %q, want %q", status, "dropped")
		}
	})
}

func TestReclaimLeavesAnInstanceInsideItsGraceAlone(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, s, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		finishContest(t, ctx, contest.ID, 5)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		// Reclaimable excludes it, so the cluster must not even be asked.
		if _, _, called := fake.outcomeOf(database); called {
			t.Fatalf("the cluster was asked about %s inside its grace; result = %+v", database, result)
		}
		if status := statusOf(t, ctx, database); status == "dropped" {
			t.Fatal("an instance inside its grace was marked dropped")
		}
		if entries := entriesFor(s, contest.ID); len(entries) != 0 {
			t.Fatalf("an instance inside its grace was audited as reclaimed: %+v", entries)
		}
	})
}

func TestReclaimNeverTouchesAContestThatHasNotFinished(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 2); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		// The contest stays in draft.
		var databases []string
		for _, inst := range instancesOf(t, ctx, contest.ID) {
			databases = append(databases, inst.Database)
		}
		if len(databases) != 2 {
			t.Fatalf("setup: %d instances, want 2", len(databases))
		}

		if _, err := service.Reclaim(ctx, 0); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		// Grace 0, yet a contest that is not finished or archived is never
		// offered, so the cluster must not even be asked.
		for _, database := range databases {
			if _, _, called := fake.outcomeOf(database); called {
				t.Fatalf("the cluster was asked about %s of a contest that never finished", database)
			}
			if status := statusOf(t, ctx, database); status == "dropped" {
				t.Fatalf("%s was reclaimed although its contest never finished", database)
			}
		}
	})
}

// internal/gamedb's tests prove the underlying refusal on a real cluster.
func TestReclaimLeavesABusyDatabaseForTheNextTick(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, s, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		finishContest(t, ctx, contest.ID, 120)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		// Asked per database rather than via result.Skipped, which also counts
		// other contests' rows in the same pass.
		dropped, dropErr, called := fake.outcomeOf(database)
		if !called {
			t.Fatalf("the cluster was never asked about %s; result = %+v", database, result)
		}
		if dropped || dropErr != nil {
			t.Fatalf("DropIdle(%s) = (%v, %v), want (false, nil) for a busy database", database, dropped, dropErr)
		}
		if status := statusOf(t, ctx, database); status == "dropped" {
			t.Fatal("a busy database's row was marked dropped anyway")
		}
		if entries := entriesFor(s, contest.ID); len(entries) != 0 {
			t.Fatalf("a busy database was audited as reclaimed: %+v", entries)
		}
	})
}

func TestReclaimDoesNotCallABusyDatabaseStuckRightAfterItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		// 60-minute grace, finished 120 minutes ago: one hour overdue, far
		// below stuckAfter's 24 hours.
		finishContest(t, ctx, contest.ID, 120)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		if _, found := stuckEntryFor(result, database); found {
			t.Fatalf("%s was named stuck this soon past the grace; stuck = %+v", database, result.Stuck)
		}
	})
}

func TestReclaimNamesADatabaseStuckFarPastItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		// 60-minute grace, finished 2000 minutes ago: about 32 hours overdue.
		finishContest(t, ctx, contest.ID, 2000)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		dropped, dropErr, called := fake.outcomeOf(database)
		if !called || dropped || dropErr != nil {
			t.Fatalf("DropIdle(%s) = (%v, %v, called=%v), want (false, nil, true)", database, dropped, dropErr, called)
		}
		stuck, found := stuckEntryFor(result, database)
		if !found {
			t.Fatalf("%s was not named stuck; stuck = %+v", database, result.Stuck)
		}
		if stuck.ContestID != contest.ID {
			t.Fatalf("stuck entry = %+v, want contest=%s", stuck, contest.ID)
		}
		if stuck.Overdue < 24*time.Hour {
			t.Fatalf("overdue = %s, want more than 24h", stuck.Overdue)
		}
	})
}

func TestReclaimOneFailureLeavesTheRestReclaimed(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 2); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		instances := instancesOf(t, ctx, contest.ID)
		if len(instances) != 2 {
			t.Fatalf("setup: %d instances, want 2", len(instances))
		}
		broken, other := instances[0].Database, instances[1].Database
		fake.failIdleDropOf(broken, errors.New("the cluster refused"))
		finishContest(t, ctx, contest.ID, 120)

		if _, err := service.Reclaim(ctx, 60); err == nil {
			t.Fatal("a failing instance produced no error from Reclaim")
		}
		dropped, dropErr, called := fake.outcomeOf(broken)
		if !called || dropped || dropErr == nil {
			t.Fatalf("DropIdle(%s) = (%v, %v, called=%v), want (false, non-nil, true)", broken, dropped, dropErr, called)
		}
		if status := statusOf(t, ctx, broken); status == "dropped" {
			t.Fatal("a database whose drop failed was marked dropped anyway")
		}
		if !wasDropped(fake, other) {
			t.Fatalf("%s was not dropped — one failure must not stop the rest", other)
		}
		if status := statusOf(t, ctx, other); status != "dropped" {
			t.Fatalf("the other instance's status = %q, want dropped — one failure must not stop the rest", status)
		}
	})
}

func TestReclaimDropsATemplateOnceItsInstancesAreGone(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, s, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		templateDB := "game_tpl_" + uuid.NewString()[:12]
		markTemplateReady(t, ctx, contest.ID, templateDB)
		finishContest(t, ctx, contest.ID, 120)

		if _, err := service.Reclaim(ctx, 60); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		if !wasDropped(fake, database) {
			t.Fatalf("the instance %s was not dropped", database)
		}
		if status := statusOf(t, ctx, database); status != "dropped" {
			t.Fatalf("instance status = %q, want %q", status, "dropped")
		}
		if !wasDropped(fake, templateDB) {
			t.Fatalf("the template %s was not dropped", templateDB)
		}
		if status := templateStatusOf(t, ctx, contest.ID); status != "dropped" {
			t.Fatalf("template status = %q, want %q", status, "dropped")
		}

		// The instance must be dropped before its template.
		drops := fake.idleDrops()
		instanceIdx, templateIdx := -1, -1
		for i, d := range drops {
			switch d {
			case database:
				instanceIdx = i
			case templateDB:
				templateIdx = i
			}
		}
		if instanceIdx == -1 || templateIdx == -1 {
			t.Fatalf("drops = %v, want both %s and %s in it", drops, database, templateDB)
		}
		if instanceIdx >= templateIdx {
			t.Fatalf("the instance is at index %d and its template at %d in %v, want the instance first",
				instanceIdx, templateIdx, drops)
		}

		entries := entriesFor(s, contest.ID)
		if len(entries) != 2 {
			t.Fatalf("%d audit entries were written about this contest, want 2 (the instance and the template): %+v", len(entries), entries)
		}
		templateEntry := entries[1]
		if templateEntry.Action != audit.ActionGameTemplateReclaim || templateEntry.EntityID != contest.ID.String() {
			t.Fatalf("template audit entry = %+v; wrong action or entity", templateEntry)
		}
		if templateEntry.Payload["database"] != templateDB {
			t.Fatalf("template payload database = %v, want %q", templateEntry.Payload["database"], templateDB)
		}
	})
}

func TestReclaimLeavesTheTemplateAloneWhileAnInstanceIsStillBusy(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		templateDB := "game_tpl_" + uuid.NewString()[:12]
		markTemplateReady(t, ctx, contest.ID, templateDB)
		finishContest(t, ctx, contest.ID, 120)

		if _, err := service.Reclaim(ctx, 60); err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		// ReclaimableTemplates excludes it while an instance is live, so the
		// cluster must not even be asked.
		if _, _, called := fake.outcomeOf(templateDB); called {
			t.Fatal("the cluster was asked about the template while an instance still needed it")
		}
		if status := templateStatusOf(t, ctx, contest.ID); status == "dropped" {
			t.Fatal("the template was marked dropped while an instance still needed it")
		}
	})
}
