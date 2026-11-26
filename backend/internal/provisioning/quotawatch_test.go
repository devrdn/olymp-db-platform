package provisioning_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/postgres"
	"github.com/devrdn/olymp-db-platform/backend/internal/provisioning"
	"github.com/devrdn/olymp-db-platform/backend/internal/sqlpolicy"
	"github.com/google/uuid"
)

// WatchQuotas measures every candidate in the shared database, not only the
// test's own, so these tests run inside withRollback and read the sweep, the
// fake cluster and the sink for their own rows only.

// templateMiB is what the fake cluster says every template measures, so a copy
// under the default ratio of five may grow to 20 MiB.
const templateMiB = 4

// writes counts the transitions the service asks the repository for, by
// database, so a test can tell "nothing written" from "written and refused".
type writes struct {
	provisioning.Repository
	marks, clears map[string]int
	listFail      error
}

func (w *writes) MarkOverQuota(ctx context.Context, database string, read time.Time) (bool, error) {
	w.marks[database]++
	return w.Repository.MarkOverQuota(ctx, database, read)
}

func (w *writes) ClearOverQuota(ctx context.Context, database string, read time.Time) (bool, error) {
	w.clears[database]++
	return w.Repository.ClearOverQuota(ctx, database, read)
}

func (w *writes) QuotaCandidates(ctx context.Context, after string, limit int) ([]provisioning.QuotaCandidate, error) {
	if w.listFail != nil {
		return nil, w.listFail
	}
	return w.Repository.QuotaCandidates(ctx, after, limit)
}

// watched is a running read_write contest with copies its participants can
// fill, and the service that watches them.
type watched struct {
	service   *provisioning.Service
	cluster   *cluster
	trail     *sink
	repo      *writes
	contest   provisioning.Contest
	people    []uuid.UUID
	databases []string
}

// watchedContest makes a running read_write contest with a ready template and
// one claimed copy per registration, through ctx's transaction. ratio 0
// leaves the policy's default.
func watchedContest(t *testing.T, ctx context.Context, registrations, ratio int) watched {
	t.Helper()

	contest, people := contestFor(t, ctx, registrations)
	q := storage.QuerierFrom(ctx, testPool)
	if _, err := q.Exec(ctx, `UPDATE contests SET status = 'running' WHERE id = $1`, contest.ID); err != nil {
		t.Fatalf("start the contest: %v", err)
	}
	if ratio == 0 {
		ratio = sqlpolicy.DefaultDiskQuotaRatio
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO contest_sql_policies (contest_id, mode, writable_tables, disk_quota_ratio)
		 VALUES ($1, 'read_write', '{evidence}', $2)`, contest.ID, ratio); err != nil {
		t.Fatalf("make the contest read_write: %v", err)
	}
	contest.Template = "quota_tpl_" + uuid.NewString()[:12]
	markTemplateReady(t, ctx, contest.ID, contest.Template)
	contest.Policy = sqlpolicy.ReadWrite("evidence")
	contest.Policy.DiskQuotaRatio = ratio

	games := postgres.NewGameInstances(testPool)
	w := watched{
		cluster: &cluster{},
		trail:   &sink{},
		repo:    &writes{Repository: games, marks: map[string]int{}, clears: map[string]int{}},
		contest: contest,
		people:  people,
	}
	w.cluster.setTemplateBytes(templateMiB << 20)
	w.service = provisioning.New(w.repo, w.cluster, audit.New(w.trail), storage.NewUnitOfWork(testPool)).
		WithWorkers(1)

	for _, registration := range people {
		database := "game_quota_" + uuid.NewString()[:12]
		if err := games.Assign(ctx, contest.ID, registration, database, contest.Version); err != nil {
			t.Fatalf("assign %s: %v", database, err)
		}
		w.databases = append(w.databases, database)
	}
	return w
}

func (w watched) sweep(t *testing.T, ctx context.Context) provisioning.QuotaSweep {
	t.Helper()
	sweep, err := w.service.WatchQuotas(ctx)
	if err != nil {
		t.Fatalf("WatchQuotas: %v", err)
	}
	return sweep
}

// flag reads the row's stored over_quota_since, before forOrganiser decides
// what an organiser is shown.
func (w watched) flag(t *testing.T, ctx context.Context, database string) *time.Time {
	t.Helper()
	record, err := postgres.NewGameInstances(testPool).InstanceNamed(ctx, w.contest.ID, database)
	if err != nil {
		t.Fatalf("read %s: %v", database, err)
	}
	return record.OverQuotaSince
}

func transitionOf(list []provisioning.QuotaTransition, database string) (provisioning.QuotaTransition, bool) {
	i := slices.IndexFunc(list, func(t provisioning.QuotaTransition) bool { return t.Database == database })
	if i < 0 {
		return provisioning.QuotaTransition{}, false
	}
	return list[i], true
}

// At its quota the runner already refuses a growing write, so the monitor
// flags it there too, not one byte later.
func TestAWatchedCopyAtItsQuotaIsFlaggedAndRecorded(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 0)
		database := w.databases[0]
		quota := int64(templateMiB*sqlpolicy.DefaultDiskQuotaRatio) << 20
		w.cluster.setSize(database, quota)

		sweep := w.sweep(t, ctx)

		got, ok := transitionOf(sweep.Exceeded, database)
		if !ok {
			t.Fatalf("a copy at its quota was not flagged: %+v", sweep)
		}
		if got.SizeBytes != quota || got.QuotaBytes != quota || got.ContestID != w.contest.ID || got.Registration != w.people[0] {
			t.Fatalf("the transition came back %+v", got)
		}
		if sweep.OverQuota < 1 || !sweep.Complete {
			t.Fatalf("the sweep counted %d over quota (complete %v), want this copy among them", sweep.OverQuota, sweep.Complete)
		}
		if w.flag(t, ctx, database) == nil {
			t.Fatal("the organiser's list does not show the flag")
		}

		entries := entriesFor(w.trail, w.contest.ID)
		if len(entries) != 1 {
			t.Fatalf("%d audit entries about this contest, want 1: %+v", len(entries), entries)
		}
		entry := entries[0]
		if entry.Action != audit.ActionGameInstanceOverQuota || entry.Entity != "contest" || entry.ActorID != nil {
			t.Fatalf("the entry came back %+v", entry)
		}
		for key, want := range map[string]any{
			"database": database, "registration_id": w.people[0].String(),
			"size_bytes": quota, "quota_bytes": quota,
		} {
			if entry.Payload[key] != want {
				t.Errorf("payload[%q] = %v, want %v", key, entry.Payload[key], want)
			}
		}
	})
}

// The quota is the contest's own ratio times the template, as on the runner's
// path (Service.Quota), not a number of the monitor's.
func TestTheWatchedQuotaIsTheContestsOwnRatio(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 6)
		database := w.databases[0]
		quota := int64(templateMiB*6) << 20
		w.cluster.setSize(database, quota)

		got, ok := transitionOf(w.sweep(t, ctx).Exceeded, database)
		if !ok || got.QuotaBytes != quota {
			t.Fatalf("a copy at six times its template under a ratio of 6 came back %+v (flagged %v)", got, ok)
		}
	})
}

func TestAFlaggedCopyBackWithinItsQuotaIsClearedAndRecorded(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 0)
		database := w.databases[0]
		w.cluster.setSize(database, 30<<20)
		w.sweep(t, ctx)

		w.cluster.setSize(database, 6<<20)
		sweep := w.sweep(t, ctx)

		got, ok := transitionOf(sweep.Restored, database)
		if !ok || got.SizeBytes != 6<<20 {
			t.Fatalf("a copy back within its quota came back %+v (cleared %v)", got, ok)
		}
		if w.flag(t, ctx, database) != nil {
			t.Fatal("the flag is still set")
		}
		entries := entriesFor(w.trail, w.contest.ID)
		if len(entries) != 2 || entries[1].Action != audit.ActionGameInstanceWithinQuota {
			t.Fatalf("the trail holds %+v, want the flag and then its clearing", entries)
		}
	})
}

// A tick that finds every row as it left it asks for no write at all
// (CLAUDE.md rule 6).
func TestAnUnchangedTickWritesNothing(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 2, 0)
		over, within := w.databases[0], w.databases[1]
		w.cluster.setSize(over, 30<<20)
		w.cluster.setSize(within, 6<<20)

		w.sweep(t, ctx)
		since := w.flag(t, ctx, over)
		second := w.sweep(t, ctx)

		if w.repo.marks[over] != 1 || w.repo.clears[over] != 0 {
			t.Fatalf("the over-quota copy was written %d/%d times, want one mark", w.repo.marks[over], w.repo.clears[over])
		}
		if w.repo.marks[within] != 0 || w.repo.clears[within] != 0 {
			t.Fatalf("the copy within its quota was written %d/%d times, want none", w.repo.marks[within], w.repo.clears[within])
		}
		if _, ok := transitionOf(second.Exceeded, over); ok {
			t.Fatal("the second tick reported the same transition again")
		}
		if again := w.flag(t, ctx, over); again == nil || !again.Equal(*since) {
			t.Fatalf("the flag moved from %v to %v on an unchanged tick", since, again)
		}
		if entries := entriesFor(w.trail, w.contest.ID); len(entries) != 1 {
			t.Fatalf("%d audit entries after two ticks, want the one transition", len(entries))
		}
	})
}

// A database missing from the cluster was dropped since it was listed: it is
// neither within nor over, so its row is left as it is and the tick goes on.
func TestACopyDroppedMeanwhileIsSkipped(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 2, 0)
		gone, other := w.databases[0], w.databases[1]
		w.cluster.setSize(gone, 30<<20)
		w.sweep(t, ctx)

		w.cluster.vanish(gone)
		w.cluster.setSize(other, 30<<20)
		sweep := w.sweep(t, ctx)

		if _, ok := transitionOf(sweep.Restored, gone); ok || w.repo.clears[gone] != 0 {
			t.Fatal("a database that is gone was cleared as if it had shrunk")
		}
		if _, ok := transitionOf(sweep.Exceeded, other); !ok {
			t.Fatal("the tick stopped at the missing database")
		}
		if !sweep.Complete {
			t.Fatal("a database dropped meanwhile made the sweep incomplete")
		}
	})
}

// A reset remakes the copy and an organiser's drop removes it, so neither may
// leave a flag measured on what was there before.
func TestAResetAndADropLiftTheFlag(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 2, 0)
		reset, dropped := w.databases[0], w.databases[1]
		w.cluster.setSize(reset, 30<<20)
		w.cluster.setSize(dropped, 30<<20)
		w.sweep(t, ctx)

		if err := w.service.Reset(ctx, uuid.New(), w.contest, w.people[0]); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := w.service.DropInstance(ctx, uuid.New(), w.contest.ID, dropped); err != nil {
			t.Fatalf("drop: %v", err)
		}

		for _, database := range w.databases {
			if since := w.flag(t, ctx, database); since != nil {
				t.Errorf("%s still reads over quota since %v", database, since)
			}
		}
	})
}

// One DatabaseSizes round trip per batch, however many copies a contest has,
// and every copy measured.
func TestWatchQuotasMeasuresInBatches(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, provisioning.QuotaBatchLimit+1, 0)
		w.sweep(t, ctx)

		batches := w.cluster.sizeBatches()
		if len(batches) < 2 {
			t.Fatalf("%d copies were measured in %d round trip(s)", len(w.databases), len(batches))
		}
		for _, n := range batches {
			if n > provisioning.QuotaBatchLimit {
				t.Fatalf("one round trip asked for %d sizes, past the batch of %d", n, provisioning.QuotaBatchLimit)
			}
		}
		sized, _ := w.cluster.sizeReads()
		for _, database := range w.databases {
			if !slices.Contains(sized, database) {
				t.Fatalf("%s was never measured", database)
			}
		}
	})
}

func TestAFailedMeasurementLeavesTheSweepIncomplete(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 0)
		w.cluster.setSize(w.databases[0], 30<<20)
		failure := errors.New("the game cluster is away")
		w.cluster.sizesFail = failure

		sweep, err := w.service.WatchQuotas(ctx)
		if !errors.Is(err, failure) {
			t.Fatalf("WatchQuotas = %v, want %v", err, failure)
		}
		if sweep.Complete || w.repo.marks[w.databases[0]] != 0 {
			t.Fatalf("a tick that measured nothing came back complete %v with %d marks", sweep.Complete, w.repo.marks[w.databases[0]])
		}

		w.cluster.sizesFail = nil
		w.repo.listFail = failure
		if sweep, err := w.service.WatchQuotas(ctx); !errors.Is(err, failure) || sweep.Complete {
			t.Fatalf("a tick that could not list the copies = %v, complete %v", err, sweep.Complete)
		}
	})
}

// The cluster's total is read on every tick and set against
// GAME_CLUSTER_MAX_BYTES, whatever the copies measured.
func TestWatchQuotasReportsTheClusterAgainstItsBudget(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 0, 0)
		w.service.WithClusterBudget(100 << 20)
		w.cluster.fillTo(80 << 20)

		sweep := w.sweep(t, ctx)
		want := provisioning.ClusterUsage{Bytes: 80 << 20, Budget: 100 << 20, Known: true}
		if sweep.Cluster != want {
			t.Fatalf("the cluster came back %+v, want %+v", sweep.Cluster, want)
		}

		failure := errors.New("the game cluster is away")
		w.cluster.clusterBytesFail = failure
		// A new service, since the first holds its measurement for a while.
		fresh := provisioning.New(w.repo, w.cluster, audit.New(w.trail), storage.NewUnitOfWork(testPool)).
			WithClusterBudget(100 << 20)
		sweep, err := fresh.WatchQuotas(ctx)
		if !errors.Is(err, failure) || sweep.Cluster.Known {
			t.Fatalf("an unreadable cluster = %v, %+v", err, sweep.Cluster)
		}
		if !sweep.Complete {
			t.Fatal("an unreadable cluster total stopped the copies being measured")
		}
	})
}

// A template whose database is missing fails Quota for every copy of its
// contest; it is asked once per tick and reported once, not once per copy.
func TestAQuotaThatCannotBeWorkedOutIsAskedAndReportedOncePerContest(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 3, 0)
		failure := errors.New("the template is gone")
		w.cluster.templateFail = failure

		sweep, err := w.service.WatchQuotas(ctx)
		if !errors.Is(err, failure) || sweep.Complete {
			t.Fatalf("WatchQuotas = %v, complete %v; want the failure reported", err, sweep.Complete)
		}
		if asked := w.cluster.templateReadsOf(w.contest.Template); asked != 1 {
			t.Fatalf("the template was measured %d times for three copies, want once", asked)
		}
		if n := strings.Count(err.Error(), w.contest.ID.String()); n != 1 {
			t.Fatalf("the contest is named %d times in the failure, want once: %v", n, err)
		}
	})
}

// A cluster that stops answering ends the tick at its deadline as a failure,
// rather than holding it for the pool's statement timeout.
func TestAHungClusterEndsTheSweepAtItsDeadline(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 0)
		w.service.WithClusterBudget(64 << 30)
		w.cluster.hangSizes = true

		bounded, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		sweep, err := w.service.WatchQuotas(bounded)
		if !errors.Is(err, context.DeadlineExceeded) || sweep.Complete {
			t.Fatalf("WatchQuotas on a hung cluster = %v, complete %v", err, sweep.Complete)
		}
		if took := time.Since(started); took > 5*time.Second {
			t.Fatalf("the sweep outlived its deadline by %s", took)
		}
		// Past its deadline the tick does not start the cluster's scan.
		if reads := w.cluster.clusterByteReads(); reads != 0 {
			t.Fatalf("a tick past its deadline still read the cluster %d time(s)", reads)
		}
	})
}

// The pool tender measures the cluster with no deadline of its own (its
// copies take minutes), and the measurement is shared under one lock. A
// sweep queued behind a hung measurement must still end at its own deadline,
// on the default configuration where a budget is set.
func TestASweepQueuedBehindAHungClusterMeasurementEndsAtItsDeadline(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 1, 0)
		w.service.WithClusterBudget(64 << 30)
		w.cluster.hangCluster = true

		// The tender's path: RosterDepth measures the cluster to size a pool.
		// It runs outside the test's transaction, which one goroutine owns.
		// Bounded, so a lock that ignores the sweep's deadline fails the test
		// below instead of hanging it.
		holding, release := context.WithTimeout(context.Background(), 4*time.Second)
		defer release()
		depth := w.service.RosterDepth(provisioning.PoolLimits{Headroom: 1, MaxClusterBytes: 64 << 30}, nil)
		held := make(chan struct{})
		go func() {
			defer close(held)
			_, _ = depth(holding, w.contest)
		}()
		for deadline := time.Now().Add(5 * time.Second); w.cluster.clusterReadsWaiting() == 0; {
			if time.Now().After(deadline) {
				t.Fatal("setup: the tender never started its measurement")
			}
			time.Sleep(5 * time.Millisecond)
		}

		bounded, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		started := time.Now()
		sweep, err := w.service.WatchQuotas(bounded)
		if !errors.Is(err, context.DeadlineExceeded) || sweep.Cluster.Known {
			t.Fatalf("a sweep behind a hung measurement = %v, %+v; want its own deadline", err, sweep.Cluster)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Fatalf("the sweep waited %s behind the tender's measurement", took)
		}
		release()
		<-held
	})
}

// With GAME_CLUSTER_MAX_BYTES=0 nothing reads the cluster's total, so the
// monitor does not scan every database's files for a figure nobody compares.
func TestWithoutABudgetTheClusterIsNotMeasured(t *testing.T) {
	withRollback(t, func(ctx context.Context) {
		w := watchedContest(t, ctx, 0, 0)
		w.cluster.fillTo(80 << 20)

		sweep := w.sweep(t, ctx)
		if w.cluster.clusterByteReads() != 0 || sweep.Cluster.Known {
			t.Fatalf("with no budget the cluster was read %d times (%+v)", w.cluster.clusterByteReads(), sweep.Cluster)
		}
	})
}

func TestTheClusterIsNearItsBudgetFromEightyPercent(t *testing.T) {
	for _, tc := range []struct {
		usage provisioning.ClusterUsage
		near  bool
	}{
		{provisioning.ClusterUsage{Bytes: 79, Budget: 100, Known: true}, false},
		{provisioning.ClusterUsage{Bytes: 80, Budget: 100, Known: true}, true},
		{provisioning.ClusterUsage{Bytes: 120, Budget: 100, Known: true}, true},
		{provisioning.ClusterUsage{Bytes: 80 << 30, Budget: 100 << 30, Known: true}, true},
		// A budget under a hundred bytes keeps its threshold.
		{provisioning.ClusterUsage{Bytes: 39, Budget: 50, Known: true}, false},
		{provisioning.ClusterUsage{Bytes: 40, Budget: 50, Known: true}, true},
		// No budget configured: nothing to be near.
		{provisioning.ClusterUsage{Bytes: 80, Budget: 0, Known: true}, false},
		// Not measured: no claim either way.
		{provisioning.ClusterUsage{Bytes: 80, Budget: 100}, false},
	} {
		if got := tc.usage.NearBudget(); got != tc.near {
			t.Errorf("%+v: NearBudget = %v, want %v", tc.usage, got, tc.near)
		}
	}
}
