package provisioning_test

import (
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// The disk budget used to bind only the background tender, so every
// participant past the point where the pool stopped growing was handed a copy
// nobody had checked there was room for. At the scale this platform states —
// a three-gigabyte template, three hundred participants, a 64 GiB default —
// that is two hundred and eighty copies and eight hundred and forty gigabytes
// onto a volume with no size of its own, until PostgreSQL stops for everybody.
func TestALateRegistrationIsRefusedWhenTheClusterIsFull(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 63 << 30}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).
		WithClusterBudget(64 << 30)

	_, err := service.Ensure(t.Context(), contest, people[0])
	if !errors.Is(err, provisioning.ErrClusterFull) {
		t.Fatalf("ensure past the budget = %v, want ErrClusterFull", err)
	}
	if made, _ := fake.counts(); made != 0 {
		t.Fatalf("the cluster made %d databases after refusing to", made)
	}
}

// The refusal has to be exactly at the boundary and not near it: a budget with
// room for one more copy still hands one out.
func TestALateRegistrationIsAllowedWhileThereIsRoom(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 60 << 30}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).
		WithClusterBudget(64 << 30)

	database, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("ensure with room left: %v", err)
	}
	if database == "" {
		t.Fatal("no database was provided")
	}
}

// A deployment that configured no budget is the state this platform shipped
// in, and it must keep working exactly as it did — including not asking the
// cluster how full it is on a path that never needed to know.
func TestNoBudgetRefusesNothingAndMeasuresNothing(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 1 << 60}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)

	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("ensure with no budget configured: %v", err)
	}
	if reads := fake.clusterByteReads(); reads != 0 {
		t.Fatalf("the cluster was measured %d times with no budget to measure against", reads)
	}
}

// A copy that already exists is rebuilt under its own name — CreateInstance
// drops the old one first — so the cluster does not grow and a full cluster
// must not take a database away from somebody who already had one.
func TestARebuildIsNotRefusedByTheBudget(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake).
		WithClusterBudget(64 << 30)

	first, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}

	// The cluster fills up, and the game is rebuilt under a newer version.
	fake.fillTo(1 << 60)
	contest.Version++

	second, err := service.Ensure(t.Context(), contest, people[0])
	if err != nil {
		t.Fatalf("rebuilding an existing copy on a full cluster: %v", err)
	}
	if second != first {
		t.Fatalf("the rebuild changed the database name: %q then %q", first, second)
	}
}

// `SELECT pg_database_size(...)` walks the database's own directory — hundreds
// of stat(2) calls on an empty catalogue alone — and Quota is asked on every
// query of every read-write contest. A template's bytes cannot change without
// a rebuild, and a rebuild bumps the version, so once per version is the same
// answer as once per request rather than a staler one.
func TestTheTemplateIsMeasuredOncePerVersionRatherThanPerRequest(t *testing.T) {
	contest, _ := contestFor(t, t.Context(), 0)
	contest.Policy.DiskQuotaRatio = 2
	fake := &cluster{templateBytes: 4 << 20}
	service := provisioning.New(postgres.NewGameInstances(testPool), fake)

	for range 50 {
		if _, err := service.Quota(t.Context(), contest); err != nil {
			t.Fatalf("quota: %v", err)
		}
	}
	if reads := fake.templateSizeReads(); reads != 1 {
		t.Fatalf("fifty quota reads cost %d measurements of the template, want 1", reads)
	}

	// A rebuild is the only thing that can change a template's bytes, and it
	// bumps the version. The next reader must see the new figure.
	fake.setTemplateBytes(8 << 20)
	contest.Version++

	quota, err := service.Quota(t.Context(), contest)
	if err != nil {
		t.Fatalf("quota after a rebuild: %v", err)
	}
	if reads := fake.templateSizeReads(); reads != 2 {
		t.Fatalf("a rebuilt template was measured %d times in all, want 2", reads)
	}
	if want := int64(8<<20) * int64(contest.Policy.DiskQuotaRatio); quota != want {
		t.Fatalf("quota after a rebuild = %d, want %d", quota, want)
	}
}
