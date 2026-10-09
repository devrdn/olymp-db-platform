package provisioning_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/google/uuid"
)

func serviceFor(t *testing.T, registrations int) (*provisioning.Service, *cluster, provisioning.Contest, []uuid.UUID) {
	t.Helper()

	contest, people := contestFor(t, t.Context(), registrations)
	fake := &cluster{}
	return provisioning.New(postgres.NewGameInstances(testPool), fake), fake, contest, people
}

func TestAClaimFromThePoolCostsNoDatabaseWork(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	if made, err := service.TopUp(t.Context(), contest, 3); err != nil || made != 3 {
		t.Fatalf("topping up made %d (%v), want 3", made, err)
	}
	before, _ := fake.counts()

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if database == "" {
		t.Fatal("no database was provided")
	}

	after, _ := fake.counts()
	if after != before {
		t.Fatalf("providing a copy from the pool created %d databases", after-before)
	}
}

func TestAnEmptyPoolStillProducesADatabase(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure with an empty pool: %v", err)
	}
	made, _ := fake.counts()
	if made != 1 {
		t.Fatalf("the cluster made %d databases, want 1", made)
	}
	if database == "" {
		t.Fatal("no database was provided")
	}
}

func TestAskingTwiceGivesTheSameDatabase(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	first, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if first != second {
		t.Fatalf("two databases for one participant: %q then %q", first, second)
	}
	if made, _ := fake.counts(); made != 1 {
		t.Fatalf("the cluster made %d databases for one participant", made)
	}
}

func TestToppingUpCountsWhatIsAlreadyThere(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 0)

	if made, err := service.TopUp(t.Context(), contest, 4); err != nil || made != 4 {
		t.Fatalf("first top-up made %d (%v), want 4", made, err)
	}
	if made, err := service.TopUp(t.Context(), contest, 4); err != nil || made != 0 {
		t.Fatalf("second top-up made %d (%v), want none", made, err)
	}
	if spare, err := service.Databases(t.Context(), contest); err != nil || spare != 4 {
		t.Fatalf("pool depth = %d (%v), want 4", spare, err)
	}
}

func TestARebuildTakesEveryDatabaseWithIt(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	if _, err := service.TopUp(t.Context(), contest, 2); err != nil {
		t.Fatalf("top-up: %v", err)
	}
	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	rebuilt := contest
	rebuilt.Version = 2

	if ready, err := service.Ready(t.Context(), rebuilt); err != nil || ready {
		t.Fatalf("ready = %v (%v); a contest may not start on stale databases", ready, err)
	}

	dropped, err := service.Invalidate(t.Context(), rebuilt)
	if err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d databases, want the claimed one and the spare", dropped)
	}
	if _, actually := fake.counts(); actually < 2 {
		t.Fatalf("the cluster dropped %d databases", actually)
	}

	if ready, err := service.Ready(t.Context(), rebuilt); err != nil || !ready {
		t.Fatalf("ready = %v (%v) after invalidating", ready, err)
	}
}

func TestAParticipantOnAnOldTemplateIsMovedOnFirstAsking(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	before, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	rebuilt := contest
	rebuilt.Version = 2

	after, err := service.Ensure(t.Context(), rebuilt, people[0])
	if err != nil {
		t.Fatalf("ensure after a rebuild: %v", err)
	}
	if after != before {
		t.Fatalf("the database was renamed: %q then %q", before, after)
	}
	if made, _ := fake.counts(); made != 2 {
		t.Fatalf("the cluster made %d databases; the second should have been rebuilt", made)
	}
	if ready, err := service.Ready(t.Context(), rebuilt); err != nil || !ready {
		t.Fatalf("ready = %v (%v); the participant is still recorded as stale", ready, err)
	}
}

// The dropped row still carries the current version, so only its status says
// the database is gone.
func TestEnsureRebuildsAnInstanceTheSweepAlreadyDropped(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	first, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if err := postgres.NewGameInstances(testPool).MarkDropped(t.Context(), first); err != nil {
		t.Fatalf("marking dropped: %v", err)
	}

	second, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure after the sweep dropped it: %v", err)
	}
	if second != first {
		t.Fatalf("a new name was chosen: %q then %q; the row should have been rebuilt under its own name", first, second)
	}
	if made, _ := fake.counts(); made != 2 {
		t.Fatalf("the cluster made %d databases; the second Ensure should have rebuilt it rather than handing back the dropped row", made)
	}

	instance, err := postgres.NewGameInstances(testPool).Of(t.Context(), people[0])
	if err != nil {
		t.Fatalf("reading the instance back: %v", err)
	}
	if instance.Status == provisioning.InstanceStatusDropped {
		t.Fatal("the row is still marked dropped after Ensure rebuilt it")
	}
}

func TestADatabaseIsNotLeftBehindWhenItCannotBeRecorded(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{}
	// An unknown contest makes the real insert fail on its foreign key.
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)

	broken := contest
	broken.ID = uuid.New() // no such contest, so recording cannot succeed

	if _, err := service.Ensure(t.Context(), broken, people[0]); err == nil {
		t.Fatal("a database was provided although nothing could be recorded")
	}
	made, dropped := fake.counts()
	if made != 1 || dropped != 1 {
		t.Fatalf("made %d and dropped %d; the unrecorded database was left behind", made, dropped)
	}
}

func TestResettingKeepsTheNameAndRemakesTheDatabase(t *testing.T) {
	service, fake, contest, people := serviceFor(t, 1)

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	before, _ := fake.counts()

	if err := service.Reset(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("reset: %v", err)
	}

	after, _ := fake.counts()
	if after != before+1 {
		t.Fatal("resetting did not remake the database")
	}
	again, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure after reset: %v", err)
	}
	if again != database {
		t.Fatalf("the name changed on reset: %q then %q", database, again)
	}
}

func TestResettingSomethingThatWasNeverProvisioned(t *testing.T) {
	service, _, contest, people := serviceFor(t, 1)

	if err := service.Reset(t.Context(), contest, people[0]); !errors.Is(err, provisioning.ErrNoInstance) {
		t.Fatalf("error = %v, want ErrNoInstance", err)
	}
}

func TestTheQuotaFollowsTheTemplateAndHasAFloor(t *testing.T) {
	service, _, contest, _ := serviceFor(t, 0)

	contest.Policy.DiskQuotaRatio = 100 // the fake template is one megabyte
	large, err := service.Quota(t.Context(), contest)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if large != 100<<20 {
		t.Fatalf("quota = %d, want a hundred times the template", large)
	}

	contest.Policy.DiskQuotaRatio = 1
	small, err := service.Quota(t.Context(), contest)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if small < 16<<20 {
		t.Fatalf("quota = %d; the floor did not apply", small)
	}
}

func TestThePoolIsFilledByABoundedNumberOfWorkers(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	fake := &cluster{slow: 40 * time.Millisecond}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).WithWorkers(2)

	made, err := service.TopUp(t.Context(), contest, 8)
	if err != nil {
		t.Fatalf("top-up: %v", err)
	}
	if made != 8 {
		t.Fatalf("made %d copies, want 8", made)
	}
	if peak := fake.highWater(); peak > 2 {
		t.Fatalf("%d copies were being made at once, want at most 2", peak)
	}
	if peak := fake.highWater(); peak < 2 {
		t.Fatalf("high water was %d; the workers never overlapped, so this proves nothing", peak)
	}
}

func TestAFailureStopsTheFillingRatherThanGrindingOn(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	fake := &cluster{fail: errors.New("the cluster is out of disk")}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).WithWorkers(2)

	made, err := service.TopUp(t.Context(), contest, 6)
	if err == nil {
		t.Fatal("a refusing cluster produced no error")
	}
	if made != 0 {
		t.Fatalf("made %d copies against a cluster that refuses everything", made)
	}
}
