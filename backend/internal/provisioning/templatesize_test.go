package provisioning_test

import (
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// 63 GiB used of 64: a 3 GiB copy does not fit.
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

// A rebuild drops the old copy first, so the cluster does not grow.
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

	// A rebuild bumps the version; the next reader sees the new size.
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
