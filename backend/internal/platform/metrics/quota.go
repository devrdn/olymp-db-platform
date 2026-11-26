package metrics

import "github.com/prometheus/client_golang/prometheus"

// GameQuotaMetrics are what the quota monitor reports: participants'
// databases at or past their disk quota and their transitions, failed ticks,
// and the game cluster's size against GAME_CLUSTER_MAX_BYTES. The gauges
// describe the shared cluster, so read them with max(); the counters count one
// replica's ticks.
type GameQuotaMetrics struct {
	overQuota prometheus.Gauge
	exceeded  prometheus.Counter
	restored  prometheus.Counter
	failed    prometheus.Counter

	clusterBytes    prometheus.Gauge
	clusterMaxBytes prometheus.Gauge
	nearBudget      prometheus.Gauge
}

// NewGameQuotaMetrics registers the set on rec's registry when rec is the
// Prometheus backend, and returns metrics that discard otherwise. The cluster
// gauges are registered only with a budget, clusterMaxBytes above zero
// (GAME_CLUSTER_MAX_BYTES, 0 meaning none), which game_cluster_max_bytes then
// holds; the monitor measures the cluster only then. Call it once per registry.
func NewGameQuotaMetrics(rec Recorder, clusterMaxBytes int64) *GameQuotaMetrics {
	p, ok := rec.(*Prometheus)
	if !ok {
		return &GameQuotaMetrics{}
	}

	m := &GameQuotaMetrics{
		overQuota: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "game_instances_over_quota",
			Help: "Participant databases at or past their disk quota at the quota monitor's last complete tick.",
		}),
		exceeded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_quota_exceeded_total",
			Help: "Participant databases the quota monitor flagged as having grown to their disk quota.",
		}),
		restored: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_quota_restored_total",
			Help: "Flagged participant databases the quota monitor saw back within their disk quota.",
		}),
		failed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_quota_watch_failed_total",
			Help: "Quota monitor ticks that could not measure or record everything, a tick past its deadline included.",
		}),
	}
	p.Registry().MustRegister(m.overQuota, m.exceeded, m.restored, m.failed)
	if clusterMaxBytes <= 0 {
		return m
	}

	m.clusterBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "game_cluster_bytes",
		Help: "Disk every database on the game cluster occupies, estimated: the last short-lived measurement plus the copies this process made since.",
	})
	m.clusterMaxBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "game_cluster_max_bytes",
		Help: "GAME_CLUSTER_MAX_BYTES, the game cluster's disk budget.",
	})
	m.nearBudget = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "game_cluster_near_budget",
		Help: "1 while the game cluster is at or past the quota monitor's warning share of GAME_CLUSTER_MAX_BYTES, 0 otherwise.",
	})
	m.clusterMaxBytes.Set(float64(clusterMaxBytes))
	p.Registry().MustRegister(m.clusterBytes, m.clusterMaxBytes, m.nearBudget)
	return m
}

// SetOverQuota records how many databases a complete tick found over quota.
func (m *GameQuotaMetrics) SetOverQuota(n int) {
	if m.overQuota != nil {
		m.overQuota.Set(float64(n))
	}
}

// AddTransitions counts one tick's transitions in each direction.
func (m *GameQuotaMetrics) AddTransitions(exceeded, restored int) {
	if m.exceeded != nil {
		m.exceeded.Add(float64(exceeded))
	}
	if m.restored != nil {
		m.restored.Add(float64(restored))
	}
}

// AddFailedTick counts a tick that returned an error.
func (m *GameQuotaMetrics) AddFailedTick() {
	if m.failed != nil {
		m.failed.Inc()
	}
}

// SetCluster records the cluster's estimated size and whether it is near its
// budget. With a budget of 0 there is nothing to record.
func (m *GameQuotaMetrics) SetCluster(bytes int64, near bool) {
	if m.clusterBytes == nil {
		return
	}
	m.clusterBytes.Set(float64(bytes))
	flag := 0.0
	if near {
		flag = 1
	}
	m.nearBudget.Set(flag)
}
