package metrics

import "github.com/prometheus/client_golang/prometheus"

// GameReclaimCounters are the counters the game-database reclaim sweep
// reports: participant databases and contest templates removed after their
// grace period, failed attempts, and instances skipped because something was
// still connected. They let a dashboard or alert see a stuck sweep (Skipped
// climbing while Reclaimed stays flat), which the job's log line does not.
type GameReclaimCounters struct {
	reclaimed prometheus.Counter
	skipped   prometheus.Counter
	failed    prometheus.Counter

	templatesReclaimed prometheus.Counter
	templatesFailed    prometheus.Counter
}

// NewGameReclaimCounters registers the set on rec's registry when rec is the
// Prometheus backend, and returns counters that discard otherwise, so the
// caller never switches on the backend.
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
		skipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_reclaim_skipped_total",
			Help: "Reclaim sweep attempts left for the next tick because the database was still busy.",
		}),
		failed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_instances_reclaim_failed_total",
			Help: "Reclaim sweep attempts that failed to drop or record a database.",
		}),
		templatesReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_templates_reclaimed_total",
			Help: "Contest template databases dropped by the reclaim sweep once every instance was gone.",
		}),
		templatesFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "game_templates_reclaim_failed_total",
			Help: "Reclaim sweep attempts that failed to drop or record a template database.",
		}),
	}
	p.Registry().MustRegister(c.reclaimed, c.skipped, c.failed, c.templatesReclaimed, c.templatesFailed)
	return c
}

// AddInstances records one tick's outcome for participant databases.
func (c *GameReclaimCounters) AddInstances(reclaimed, skipped, failed int) {
	if c.reclaimed != nil {
		c.reclaimed.Add(float64(reclaimed))
	}
	if c.skipped != nil {
		c.skipped.Add(float64(skipped))
	}
	if c.failed != nil {
		c.failed.Add(float64(failed))
	}
}

// AddTemplates records one tick's outcome for contest templates, kept apart
// from instances because their volumes differ by orders of magnitude.
func (c *GameReclaimCounters) AddTemplates(reclaimed, failed int) {
	if c.templatesReclaimed != nil {
		c.templatesReclaimed.Add(float64(reclaimed))
	}
	if c.templatesFailed != nil {
		c.templatesFailed.Add(float64(failed))
	}
}
