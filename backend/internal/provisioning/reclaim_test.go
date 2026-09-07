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
func finishContest(t *testing.T, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := testPool.Exec(t.Context(),
		`UPDATE contests SET status = 'finished', updated_at = now() - make_interval(mins => $2::int) WHERE id = $1`,
		contest, ageMinutes); err != nil {
		t.Fatalf("finishing the contest: %v", err)
	}
}

// archiveContest is finishContest's counterpart for the other status
// Reclaimable now honours — an organizer moving a finished contest to its
// final resting place must not be a way to exempt it from the sweep.
func archiveContest(t *testing.T, contest uuid.UUID, ageMinutes int) {
	t.Helper()
	if _, err := testPool.Exec(t.Context(),
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
func serviceWithAudit(t *testing.T, registrations int) (*provisioning.Service, *cluster, *sink, provisioning.Contest, []uuid.UUID) {
	t.Helper()

	contest, people := contestFor(t, registrations)
	fake := &cluster{}
	s := &sink{}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).
		WithAudit(audit.New(s), storage.NewUnitOfWork(testPool))
	return service, fake, s, contest, people
}

// instancesOf lists every not-yet-dropped database of contest, for tests that
// need the real generated names TopUp produced rather than a name they chose.
func instancesOf(t *testing.T, contest uuid.UUID) []provisioning.Stale {
	t.Helper()
	// version 2 catches every copy: TopUp always makes version-1 copies here.
	found, err := postgres.NewGameInstances(testPool).Stale(t.Context(), contest, 2)
	if err != nil {
		t.Fatalf("listing instances: %v", err)
	}
	return found
}

func statusOf(t *testing.T, database string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(t.Context(),
		`SELECT status FROM game_instances WHERE db_name = $1`, database).Scan(&status); err != nil {
		t.Fatalf("reading %s: %v", database, err)
	}
	return status
}

func templateStatusOf(t *testing.T, contest uuid.UUID) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(t.Context(),
		`SELECT status FROM game_templates WHERE contest_id = $1`, contest).Scan(&status); err != nil {
		t.Fatalf("reading the template of %s: %v", contest, err)
	}
	return status
}

// The ordinary case: a contest finished well past its grace, one instance,
// nothing in the way. Covers the row ending 'dropped' rather than deleted,
// and the audit entry an organizer would find it by.
func TestReclaimDropsAnInstanceOfAContestPastItsGrace(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database

	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 1 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want reclaimed=1 skipped=0 failed=0", result)
	}
	if drops := fake.idleDrops(); len(drops) != 1 || drops[0] != database {
		t.Fatalf("the cluster dropped %v, want [%s]", drops, database)
	}
	if status := statusOf(t, database); status != "dropped" {
		t.Fatalf("status = %q, want %q — the row must survive, not be deleted", status, "dropped")
	}

	if len(s.entries) != 1 {
		t.Fatalf("%d audit entries were written, want 1", len(s.entries))
	}
	entry := s.entries[0]
	if entry.Action != audit.ActionGameInstanceReclaim || entry.Entity != "contest" || entry.EntityID != contest.ID.String() {
		t.Fatalf("audit entry = %+v; wrong action or entity", entry)
	}
	if entry.ActorID != nil {
		t.Fatal("a system sweep recorded an actor")
	}
	if entry.Payload["database"] != database {
		t.Fatalf("payload database = %v, want %q — how an organizer finds it", entry.Payload["database"], database)
	}
}

// Archiving is the "put this away" action available once a contest is
// finished, and it must not be a way to exempt a contest's instances from
// the sweep forever — the leak Reclaimable's widened status filter exists to
// close.
func TestReclaimDropsAnInstanceOfAnArchivedContestPastItsGrace(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database

	archiveContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1 for an archived contest past its grace", result.Reclaimed)
	}
	if drops := fake.idleDrops(); len(drops) != 1 || drops[0] != database {
		t.Fatalf("the cluster dropped %v, want [%s]", drops, database)
	}
}

// The grace exists to be honoured, not merely configured: an instance whose
// contest finished a moment ago must survive this tick.
func TestReclaimLeavesAnInstanceInsideItsGraceAlone(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	finishContest(t, contest.ID, 5)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 0 || result.Skipped != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v, want everything zero", result)
	}
	if drops := fake.idleDrops(); len(drops) != 0 {
		t.Fatalf("the cluster was asked to drop %v inside the grace", drops)
	}
	if status := statusOf(t, database); status == "dropped" {
		t.Fatal("an instance inside its grace was marked dropped")
	}
	if len(s.entries) != 0 {
		t.Fatal("an instance inside its grace was audited as reclaimed")
	}
}

// A contest that never finished must never lose a database to this sweep,
// however deep its pool and however loose the installation grace.
func TestReclaimNeverTouchesAContestThatHasNotFinished(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 2); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	// contestFor leaves the contest at whatever status a fresh contest
	// starts at (draft) — never touched by finishContest, on purpose.

	result, err := service.Reclaim(t.Context(), 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v against a contest that never finished, want reclaimed=0 failed=0", result)
	}
	if drops := fake.idleDrops(); len(drops) != 0 {
		t.Fatalf("the cluster was asked to drop %v of a contest that never finished", drops)
	}
}

// A database still busy — the reclaim sweep's stand-in for "a query is
// running against it right now" — is left for the next tick rather than
// forced, and counted as a skip rather than folded into silence. Cutting off
// a participant mid-query is exactly what this proves does not happen at the
// service level; internal/gamedb's own test proves the underlying refusal
// against a real cluster.
func TestReclaimLeavesABusyDatabaseForTheNextTick(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	fake.markBusy(database)
	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 0 || result.Failed != 0 {
		t.Fatalf("result = %+v against a busy database, want reclaimed=0 failed=0 — busy is not failure", result)
	}
	if result.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 — a busy database used to vanish into reclaimed=0 failed=0 unnoticed", result.Skipped)
	}
	if status := statusOf(t, database); status == "dropped" {
		t.Fatal("a busy database's row was marked dropped anyway")
	}
	if len(s.entries) != 0 {
		t.Fatal("a busy database was audited as reclaimed")
	}
}

// A database only just past its grace and still busy is the unremarkable,
// expected steady state the grace period is designed to allow — it must not
// be named as stuck on its very first skipped tick.
func TestReclaimDoesNotCallABusyDatabaseStuckRightAfterItsGrace(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	fake.markBusy(database)
	// 60-minute grace, finished 120 minutes ago: the deadline passed an hour
	// back — busy, but nowhere near stuckAfter's 24-hour bound.
	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(result.Stuck) != 0 {
		t.Fatalf("stuck = %+v, want none this soon past the grace", result.Stuck)
	}
}

// A database still busy long after its grace ran out has stopped reading as
// "the last admitted query finishing up" — that only ever explains a short
// overrun — and Reclaim is asked to name it rather than let it fold into an
// unremarkable skip count forever.
func TestReclaimNamesADatabaseStuckFarPastItsGrace(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	fake.markBusy(database)
	// 60-minute grace, finished 2000 minutes ago (~33.3h): the deadline
	// passed roughly 32.3 hours back, comfortably past the 24-hour bound.
	finishContest(t, contest.ID, 2000)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", result.Skipped)
	}
	if len(result.Stuck) != 1 {
		t.Fatalf("stuck = %+v, want exactly one entry", result.Stuck)
	}
	stuck := result.Stuck[0]
	if stuck.Database != database || stuck.ContestID != contest.ID {
		t.Fatalf("stuck entry = %+v, want database=%s contest=%s", stuck, database, contest.ID)
	}
	if stuck.Overdue < 24*time.Hour {
		t.Fatalf("overdue = %s, want more than 24h", stuck.Overdue)
	}
}

// One instance's failure must not stop the rest of the pass, and must not be
// recorded as though it succeeded.
func TestReclaimOneFailureLeavesTheRestReclaimed(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 2); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	instances := instancesOf(t, contest.ID)
	if len(instances) != 2 {
		t.Fatalf("setup: %d instances, want 2", len(instances))
	}
	broken, other := instances[0].Database, instances[1].Database
	fake.failIdleDropOf(broken, errors.New("the cluster refused"))
	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err == nil {
		t.Fatal("a failing instance produced no error from Reclaim")
	}
	if result.Reclaimed != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v, want reclaimed=1 failed=1", result)
	}
	if status := statusOf(t, broken); status == "dropped" {
		t.Fatal("a database whose drop failed was marked dropped anyway")
	}
	if status := statusOf(t, other); status != "dropped" {
		t.Fatalf("the other instance's status = %q, want dropped — one failure must not stop the rest", status)
	}
}

// The largest single database a contest owns is reclaimed too, once every
// instance copied from it is gone — §2.4's leak this closes.
func TestReclaimDropsATemplateOnceItsInstancesAreGone(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	templateDB := "game_tpl_" + uuid.NewString()[:12]
	markTemplateReady(t, contest.ID, templateDB)
	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.Reclaimed != 1 {
		t.Fatalf("reclaimed = %d, want 1", result.Reclaimed)
	}
	if result.TemplatesReclaimed != 1 || result.TemplatesFailed != 0 {
		t.Fatalf("result = %+v, want templates_reclaimed=1 templates_failed=0", result)
	}
	drops := fake.idleDrops()
	if len(drops) != 2 {
		t.Fatalf("the cluster dropped %v, want the instance and the template", drops)
	}
	if drops[0] != database || drops[1] != templateDB {
		t.Fatalf("dropped %v in order, want [%s %s] — the instance before its template", drops, database, templateDB)
	}
	if status := templateStatusOf(t, contest.ID); status != "dropped" {
		t.Fatalf("template status = %q, want %q", status, "dropped")
	}

	if len(s.entries) != 2 {
		t.Fatalf("%d audit entries were written, want 2 (the instance and the template)", len(s.entries))
	}
	templateEntry := s.entries[1]
	if templateEntry.Action != audit.ActionGameTemplateReclaim || templateEntry.EntityID != contest.ID.String() {
		t.Fatalf("template audit entry = %+v; wrong action or entity", templateEntry)
	}
	if templateEntry.Payload["database"] != templateDB {
		t.Fatalf("template payload database = %v, want %q", templateEntry.Payload["database"], templateDB)
	}
}

// A template must never be dropped while one of its own instances is still
// there — the ordering Reclaim's own doc calls out explicitly: it is what
// instances are copied from.
func TestReclaimLeavesTheTemplateAloneWhileAnInstanceIsStillBusy(t *testing.T) {
	service, fake, _, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	fake.markBusy(database)
	templateDB := "game_tpl_" + uuid.NewString()[:12]
	markTemplateReady(t, contest.ID, templateDB)
	finishContest(t, contest.ID, 120)

	result, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if result.TemplatesReclaimed != 0 {
		t.Fatalf("templates reclaimed = %d while an instance was still busy, want 0", result.TemplatesReclaimed)
	}
	for _, d := range fake.idleDrops() {
		if d == templateDB {
			t.Fatal("the template was dropped while an instance still needed it")
		}
	}
	if status := templateStatusOf(t, contest.ID); status == "dropped" {
		t.Fatal("the template was marked dropped while an instance still needed it")
	}
}
