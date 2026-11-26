package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestGameQuotaMetricsRecordOnThePrometheusBackend(t *testing.T) {
	p := NewPrometheus()
	m := NewGameQuotaMetrics(p, 100)

	m.SetOverQuota(3)
	m.SetOverQuota(2)
	m.AddTransitions(2, 1)
	m.AddTransitions(1, 0)
	m.AddFailedTick()
	m.SetCluster(85, true)

	for _, series := range []struct {
		name      string
		got, want float64
	}{
		{"game_instances_over_quota", testutil.ToFloat64(m.overQuota), 2},
		{"game_instances_quota_exceeded_total", testutil.ToFloat64(m.exceeded), 3},
		{"game_instances_quota_restored_total", testutil.ToFloat64(m.restored), 1},
		{"game_quota_watch_failed_total", testutil.ToFloat64(m.failed), 1},
		{"game_cluster_bytes", testutil.ToFloat64(m.clusterBytes), 85},
		{"game_cluster_max_bytes", testutil.ToFloat64(m.clusterMaxBytes), 100},
		{"game_cluster_near_budget", testutil.ToFloat64(m.nearBudget), 1},
	} {
		if series.got != series.want {
			t.Errorf("%s = %v, want %v", series.name, series.got, series.want)
		}
	}

	m.SetCluster(70, false)
	if got := testutil.ToFloat64(m.nearBudget); got != 0 {
		t.Errorf("game_cluster_near_budget = %v below the threshold, want 0", got)
	}
}

func TestGameQuotaMetricsDoNothingOnNonPrometheusBackends(t *testing.T) {
	for name, rec := range allBackends(t) {
		if name == "prometheus" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			m := NewGameQuotaMetrics(rec, 100)
			// None of these may panic.
			m.SetOverQuota(1)
			m.AddTransitions(1, 1)
			m.AddFailedTick()
			m.SetCluster(1, true)
		})
	}
}

// With GAME_CLUSTER_MAX_BYTES=0 the monitor does not measure the cluster, so
// no series pretends to report it.
func TestTheClusterSeriesExistOnlyWithABudget(t *testing.T) {
	for budget, want := range map[int64]bool{0: false, 64 << 30: true} {
		p := NewPrometheus()
		m := NewGameQuotaMetrics(p, budget)
		m.SetCluster(1, false) // must not panic either way

		families, err := p.Registry().Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		found := map[string]float64{}
		for _, family := range families {
			for _, metric := range family.GetMetric() {
				if g := metric.GetGauge(); g != nil {
					found[family.GetName()] = g.GetValue()
				}
			}
		}
		for _, name := range []string{"game_cluster_bytes", "game_cluster_max_bytes", "game_cluster_near_budget"} {
			if _, present := found[name]; present != want {
				t.Errorf("budget %d: %s exported = %v, want %v", budget, name, present, want)
			}
		}
		if want && found["game_cluster_max_bytes"] != float64(budget) {
			t.Errorf("game_cluster_max_bytes = %v, want the budget %d", found["game_cluster_max_bytes"], budget)
		}
	}
}
