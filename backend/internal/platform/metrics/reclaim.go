package metrics

import "github.com/prometheus/client_golang/prometheus"

// GameReclaimCounters are the two counters the game-database reclaim sweep
// reports (docs/ARCHITECTURE.md §2.4): how many participant databases it
// removed once their contest's grace period passed, and how many attempts
// failed.
//
// The sweep is a background job, not an HTTP request, so it has no route or
// status code for Recorder.ObserveRequest to key on — this is what it reports
// through instead. internal/app/background.go's own task also logs these
// counts on every tick that moved anything, the same dual reporting every
// other background job in this codebase already gives its own numbers; this
// pair exists for what a log line does not serve on its own — a dashboard or
// an alert asking "is the sweep actually running, or silently stuck".
type GameReclaimCounters struct {
	reclaimed prometheus.Counter
	failed    prometheus.Counter
}

// NewGameReclaimCounters registers the pair on rec's own registry when rec is
// the Prometheus backend, and returns a counter that discards otherwise: the
// log and none backends already carry this figure through the job's own log
// line, and neither has a registry to add a collector to. A caller that
// always gets a working *GameReclaimCounters back never needs a type switch
// of its own to find out which backend is actually running — the same
// reasoning Recorder itself is built on.
func NewGameReclaimCounters(rec Recorder) *GameReclaimCounters {
	p, ok := rec.(*Prometheus)
	if !ok {
		return &GameReclaimCounters{}
	}

	c := &GameReclaimCounters{
		reclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_reclaimed_total",
			Help: "Participant databases dropped by the reclaim sweep after their contest's grace period.",
		}),
		failed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_reclaim_failed_total",
			Help: "Reclaim sweep attempts that failed to drop or record a database.",
		}),
	}
	p.Registry().MustRegister(c.reclaimed, c.failed)
	return c
}

// Add records one tick's outcome. Safe to call regardless of backend: with no
// Prometheus counters behind it, it does nothing.
func (c *GameReclaimCounters) Add(reclaimed, failed int) {
	if c.reclaimed != nil {
		c.reclaimed.Add(float64(reclaimed))
	}
	if c.failed != nil {
		c.failed.Add(float64(failed))
	}
}
