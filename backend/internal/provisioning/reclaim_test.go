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

// finishContest backdates a fresh contest to look as though it finished
// ageMinutes ago, by writing the same updated_at a real SetStatus or
// AdvanceFinished move would have (postgres/contests.go). Reclaim reads that
// column through the real repository, so this is what stands in for "time
// passed" without waiting for the clock.
//
// ctx, like every other fixture in these tests, decides where the write lands:
// inside withRollback it is the test's own transaction, so the contest this
// finishes is a contest nobody else can see.
func finishContest(t *testing.T, ctx context.Context, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = 'finished', updated_at = now() - make_interval(mins => $2::int) WHERE id = $1`,
		contest, ageMinutes); err != nil {
		t.Fatalf("finishing the contest: %v", err)
	}
}

// archiveContest is finishContest's counterpart for the other status
// Reclaimable now honours — an organizer moving a finished contest to its
// final resting place must not be a way to exempt it from the sweep.
func archiveContest(t *testing.T, ctx context.Context, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`UPDATE contests SET status = 'archived', updated_at = now() - make_interval(mins => $2::int) WHERE id = $1`,
		contest, ageMinutes); err != nil {
		t.Fatalf("archiving the contest: %v", err)
	}
}

// sink is a minimal audit.Sink recording what it was told. Local to this
// file rather than a shared fixture: nothing else in this package needs one,
// and rule 5 keeps a fixture used by one package's own tests in that
// package's own support file.
type sink struct{ entries []audit.Entry }

func (s *sink) Append(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

func (s *sink) AppendMany(_ context.Context, entries []audit.Entry) error {
	s.entries = append(s.entries, entries...)
	return nil
}

// serviceWithAudit is serviceFor plus the audit trail wired in, for the tests
// that check what Reclaim records.
//
// One worker rather than DefaultWorkers' three, because every caller builds
// its fixture inside withRollback: a pgx transaction is one connection, and
// TopUp's own workers would use it from several goroutines at once. That the
// pool is filled by a bounded number of workers at all is a claim
// instances_test.go's high-water test makes, outside any transaction, where
// concurrency is the point; here it is only ever setup.
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

// instancesOf lists every not-yet-dropped database of contest, for tests that
// need the real generated names TopUp produced rather than a name they chose.
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

// Reclaim is installation-wide by design (its own doc): one call sweeps
// every contest in the shared database that is due, not only the one a test
// just set up. Two separate things follow from that, and they need two
// separate answers.
//
// What the pass *writes* is dealt with by withRollback (support_test.go):
// every test below runs inside a transaction that is always rolled back, so
// a sweep that marks the developer's own rows 'dropped' — which is exactly
// what these tests used to do, and the reason eleven databases are stranded
// on the development game cluster today — leaves nothing behind.
//
// What the pass *reports* is dealt with by the helpers below. A rolled-back
// transaction still sees every committed row in the installation, so a
// ReclaimResult's totals, and a fake cluster's or sink's raw contents, can
// still carry somebody else's contest alongside this test's own. Scoping is
// the lever, the same one audit_test.go's writeTrail chose for the identical
// problem: not a smaller batch limit (Reclaim's own limit stays what
// production uses, see its doc) and not a different pass order (the ordering
// is a guarantee this package tests directly elsewhere).

// wasDropped reports whether the cluster actually dropped database — built
// on outcomeOf (support_test.go) so it answers about this one database
// regardless of whatever else the same installation-wide pass processed.
func wasDropped(fake *cluster, database string) bool {
	dropped, _, _ := fake.outcomeOf(database)
	return dropped
}

// entriesFor returns just the entries s recorded about contest — s is a
// fresh sink per test, but the one Reclaim call it backs is installation-
// wide, so it can carry another contest's entry too when that other
// contest's own reclaim happened to fall in the same pass.
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

// stuckEntryFor finds database's own entry in result.Stuck, if any — the
// list can name another package's overdue candidate in the same pass, so a
// test asks for its own by name rather than trusting the list's length.
func stuckEntryFor(result provisioning.ReclaimResult, database string) (provisioning.StuckInstance, bool) {
	for _, s := range result.Stuck {
		if s.Database == database {
			return s, true
		}
	}
	return provisioning.StuckInstance{}, false
}

// The property withRollback exists for, asserted directly rather than
// described: a reclaim test must leave every row it did not create exactly as
// it found it.
//
// The bystander below is committed on its own connection, the way a
// developer's own finished contest is committed in their own database, and it
// is a genuine candidate — finished two hours ago, one 'ready' instance, well
// past the grace the sweep is given. The sweep therefore really does reach it
// (the check on `reached` is what stops this passing for the far more
// comfortable reason that Reclaim never saw it at all), really does mark it
// dropped — and, because that write is inside the test's transaction, really
// does leave the committed row alone.
//
// Before withRollback this test fails on its last assertion: the row comes
// back 'dropped' from a second connection, which is precisely the damage
// eleven databases on the development cluster are still living with.
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

// bystanderInstance commits a finished contest past its grace and one 'ready'
// instance of it, on the pool rather than on any test transaction, and
// removes it again when the test ends. It stands in for what a developer's
// own database already holds when they run `make test-db`.
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

// committedStatusOf reads a row's status on the pool, deliberately outside
// any test transaction: what this test has to know is what a second
// connection — the developer's, tomorrow — sees.
func committedStatusOf(t *testing.T, database string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM game_instances WHERE db_name = $1`, database).Scan(&status); err != nil {
		t.Fatalf("reading %s: %v", database, err)
	}
	return status
}

// The ordinary case: a contest finished well past its grace, one instance,
// nothing in the way. Covers the row ending 'dropped' rather than deleted,
// and the audit entry an organizer would find it by.
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

// Archiving is the "put this away" action available once a contest is
// finished, and it must not be a way to exempt a contest's instances from
// the sweep forever — the leak Reclaimable's widened status filter exists to
// close.
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

// The grace exists to be honoured, not merely configured: an instance whose
// contest finished a moment ago must survive this tick.
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
		// Reclaimable's own WHERE clause (internal/postgres/gameinstances.go)
		// excludes an instance still inside its grace before Reclaim ever sees
		// it, so the cluster must never even have been asked about it — no
		// membership check on idleDrops needed, the call itself must be absent.
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

// A contest that never finished must never lose a database to this sweep,
// however deep its pool and however loose the installation grace.
func TestReclaimNeverTouchesAContestThatHasNotFinished(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 2); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		// contestFor leaves the contest at whatever status a fresh contest
		// starts at (draft) — never touched by finishContest, on purpose.
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
		// Reclaimable's WHERE clause excludes any instance of a contest whose
		// status is not 'finished' or 'archived', so the cluster must never have
		// been asked about either database — a running contest's instances are
		// not even offered, never mind reclaimed.
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

// A database still busy — the reclaim sweep's stand-in for "a query is
// running against it right now" — is left for the next tick rather than
// forced, and counted as a skip rather than folded into silence. Cutting off
// a participant mid-query is exactly what this proves does not happen at the
// service level; internal/gamedb's own test proves the underlying refusal
// against a real cluster.
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
		// outcomeOf is what proves "busy is not failure" for this database
		// specifically: DropIdle must have been called (the candidate was
		// offered, not silently dropped from the batch) and must have reported
		// neither dropped nor an error — the same (false, nil) PostgreSQL itself
		// returns for a plain DROP DATABASE against a live connection. Reading
		// result.Skipped instead would also count every other reclaimable row
		// this same installation-wide pass happened to skip, which a test
		// running under `go test ./...` does not own.
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

// A database only just past its grace and still busy is the unremarkable,
// expected steady state the grace period is designed to allow — it must not
// be named as stuck on its very first skipped tick.
func TestReclaimDoesNotCallABusyDatabaseStuckRightAfterItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		// 60-minute grace, finished 120 minutes ago: the deadline passed an hour
		// back — busy, but nowhere near stuckAfter's 24-hour bound.
		finishContest(t, ctx, contest.ID, 120)

		result, err := service.Reclaim(ctx, 60)
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		// result.Stuck can legitimately carry another package's own overdue
		// candidate from the same installation-wide pass — asking for this
		// database by name is what keeps the assertion about this test's own
		// row rather than the whole pass's list.
		if _, found := stuckEntryFor(result, database); found {
			t.Fatalf("%s was named stuck this soon past the grace; stuck = %+v", database, result.Stuck)
		}
	})
}

// A database still busy long after its grace ran out has stopped reading as
// "the last admitted query finishing up" — that only ever explains a short
// overrun — and Reclaim is asked to name it rather than let it fold into an
// unremarkable skip count forever.
func TestReclaimNamesADatabaseStuckFarPastItsGrace(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		service, fake, _, contest, _ := serviceWithAudit(t, ctx, 0)
		if _, err := service.TopUp(ctx, contest, 1); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		database := instancesOf(t, ctx, contest.ID)[0].Database
		fake.markBusy(database)
		// 60-minute grace, finished 2000 minutes ago (~33.3h): the deadline
		// passed roughly 32.3 hours back, comfortably past the 24-hour bound.
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

// One instance's failure must not stop the rest of the pass, and must not be
// recorded as though it succeeded.
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

// The largest single database a contest owns is reclaimed too, once every
// instance copied from it is gone — §2.4's leak this closes.
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

		// Both belong to the same pass's single sequential drop list
		// (fake.idleDrops), so their positions in it — whatever else that same
		// installation-wide pass also dropped around them — still prove the
		// instance went before its own template, exactly as Reclaim's own doc
		// promises (reclaimTemplates runs after the instance loop).
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

// A template must never be dropped while one of its own instances is still
// there — the ordering Reclaim's own doc calls out explicitly: it is what
// instances are copied from.
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
		// ReclaimableTemplates' own NOT EXISTS clause (internal/postgres/
		// gameinstances.go) excludes a template while any of its instances is
		// still live, so the cluster must never even have been asked about this
		// template — called must be false, not merely "returned not dropped".
		if _, _, called := fake.outcomeOf(templateDB); called {
			t.Fatal("the cluster was asked about the template while an instance still needed it")
		}
		if status := templateStatusOf(t, ctx, contest.ID); status == "dropped" {
			t.Fatal("the template was marked dropped while an instance still needed it")
		}
	})
}
