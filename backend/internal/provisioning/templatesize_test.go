package provisioning_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// 63 GiB used of 64: a 3 GiB copy does not fit.
func TestALateRegistrationIsRefusedWhenTheClusterIsFull(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 63 << 30}
	service := newService(fake).
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
	service := newService(fake).
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
	service := newService(fake)

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
	service := newService(fake).
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
	service := newService(fake)

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

// stoppedClock is a clock tests move by hand.
type stoppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stoppedClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stoppedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Contest start: every latecomer's pool miss asks how full the cluster is,
// and the answer costs a scan of every database's files. One scan serves
// them all for a while, and the copies made meanwhile are added to it, so
// the budget still binds: 55 GiB measured plus three 3 GiB copies reaches
// the 64 GiB budget, and the fourth is refused although the cluster itself
// still reports 55.
func TestLateRegistrationsShareOneClusterMeasurementThatGrowsWithEachCopy(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 4)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 55 << 30}
	service := newService(fake).WithClusterBudget(64 << 30)
	clock := &stoppedClock{now: time.Now()}
	provisioning.SetClock(service, clock.read)

	for i, person := range people[:3] {
		if _, err := service.Ensure(t.Context(), contest, person); err != nil {
			t.Fatalf("copy %d of three that fit: %v", i+1, err)
		}
	}
	if reads := fake.clusterByteReads(); reads != 1 {
		t.Fatalf("three pool misses measured the cluster %d times, want 1", reads)
	}

	_, err := service.Ensure(t.Context(), contest, people[3])
	if !errors.Is(err, provisioning.ErrClusterFull) {
		t.Fatalf("the fourth copy, which the three before it left no room for = %v, want ErrClusterFull", err)
	}
	if reads := fake.clusterByteReads(); reads != 1 {
		t.Fatalf("the refusal measured the cluster again: %d reads, want 1", reads)
	}
	if made, _ := fake.counts(); made != 3 {
		t.Fatalf("the cluster holds %d copies, want 3", made)
	}

	// Once the measurement has aged out the cluster is asked again, and the
	// fake still answers 55 GiB, so the same request now fits.
	clock.advance(11 * time.Second)
	if _, err := service.Ensure(t.Context(), contest, people[3]); err != nil {
		t.Fatalf("the same copy once the measurement aged out: %v", err)
	}
	if reads := fake.clusterByteReads(); reads != 2 {
		t.Fatalf("a measurement past its TTL was not re-read: %d reads, want 2", reads)
	}
}

// Latecomers arrive together, not one after the other: the ones that find
// no measurement wait for the one scan in flight rather than each starting
// their own.
func TestConcurrentLateRegistrationsCostOneClusterMeasurement(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 8)
	fake := &cluster{templateBytes: 1 << 30, clusterBytes: 10 << 30}
	service := newService(fake).WithClusterBudget(64 << 30)

	var party sync.WaitGroup
	errs := make([]error, len(people))
	for i, person := range people {
		party.Add(1)
		go func() {
			defer party.Done()
			_, errs[i] = service.Ensure(t.Context(), contest, person)
		}()
	}
	party.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if reads := fake.clusterByteReads(); reads != 1 {
		t.Fatalf("%d concurrent pool misses measured the cluster %d times, want 1", len(people), reads)
	}
}

// The tender's sizing shares the measurement, and sees the copies the
// participants' own path made since: 60 GiB measured, one 3 GiB copy made,
// so a 64 GiB budget has room for none more.
func TestThePoolSizingSeesTheCopiesMadeSinceTheMeasurement(t *testing.T) {
	contest, people := contestFor(t, t.Context(), 1)
	fake := &cluster{templateBytes: 3 << 30, clusterBytes: 60 << 30}
	service := newService(fake).WithClusterBudget(64 << 30)

	if _, err := service.Ensure(t.Context(), contest, people[0]); err != nil {
		t.Fatalf("the one copy that fits: %v", err)
	}

	var told []provisioning.Sizing
	depth, err := service.RosterDepth(provisioning.PoolLimits{Headroom: 2, MaxClusterBytes: 64 << 30},
		func(_ context.Context, _ provisioning.Contest, s provisioning.Sizing) { told = append(told, s) },
	)(t.Context(), contest)
	if err != nil {
		t.Fatalf("sizing: %v", err)
	}
	if depth != 0 {
		t.Fatalf("granted %d spares with the cluster at the budget, want 0", depth)
	}
	if reads := fake.clusterByteReads(); reads != 1 {
		t.Fatalf("the sizing measured the cluster again: %d reads, want 1", reads)
	}
	if len(told) != 1 || told[0].Bound != provisioning.BoundDisk || told[0].ClusterBytes != 63<<30 {
		t.Fatalf("reported %+v, want the disk bound at 63 GiB used", told)
	}
}
