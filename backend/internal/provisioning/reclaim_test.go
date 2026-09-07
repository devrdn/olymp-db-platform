package provisioning_test

import (
	"context"
	"errors"
	"testing"

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

	reclaimed, failed, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed != 1 || failed != 0 {
		t.Fatalf("reclaimed=%d failed=%d, want 1 and 0", reclaimed, failed)
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

// The grace exists to be honoured, not merely configured: an instance whose
// contest finished a moment ago must survive this tick.
func TestReclaimLeavesAnInstanceInsideItsGraceAlone(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	finishContest(t, contest.ID, 5)

	reclaimed, failed, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed != 0 || failed != 0 {
		t.Fatalf("reclaimed=%d failed=%d, want 0 and 0", reclaimed, failed)
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

	reclaimed, failed, err := service.Reclaim(t.Context(), 0)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed != 0 || failed != 0 {
		t.Fatalf("reclaimed=%d failed=%d against a contest that never finished, want 0 and 0", reclaimed, failed)
	}
	if drops := fake.idleDrops(); len(drops) != 0 {
		t.Fatalf("the cluster was asked to drop %v of a contest that never finished", drops)
	}
}

// A database still busy — the reclaim sweep's stand-in for "a query is
// running against it right now" — is left for the next tick rather than
// forced. Cutting off a participant mid-query is exactly what this proves
// does not happen at the service level; internal/gamedb's own test proves the
// underlying refusal against a real cluster.
func TestReclaimLeavesABusyDatabaseForTheNextTick(t *testing.T) {
	service, fake, s, contest, _ := serviceWithAudit(t, 0)
	if _, err := service.TopUp(t.Context(), contest, 1); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	database := instancesOf(t, contest.ID)[0].Database
	fake.markBusy(database)
	finishContest(t, contest.ID, 120)

	reclaimed, failed, err := service.Reclaim(t.Context(), 60)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed != 0 || failed != 0 {
		t.Fatalf("reclaimed=%d failed=%d against a busy database, want 0 and 0 — busy is not failure", reclaimed, failed)
	}
	if status := statusOf(t, database); status == "dropped" {
		t.Fatal("a busy database's row was marked dropped anyway")
	}
	if len(s.entries) != 0 {
		t.Fatal("a busy database was audited as reclaimed")
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

	reclaimed, failed, err := service.Reclaim(t.Context(), 60)
	if err == nil {
		t.Fatal("a failing instance produced no error from Reclaim")
	}
	if reclaimed != 1 || failed != 1 {
		t.Fatalf("reclaimed=%d failed=%d, want 1 and 1", reclaimed, failed)
	}
	if status := statusOf(t, broken); status == "dropped" {
		t.Fatal("a database whose drop failed was marked dropped anyway")
	}
	if status := statusOf(t, other); status != "dropped" {
		t.Fatalf("the other instance's status = %q, want dropped — one failure must not stop the rest", status)
	}
}
