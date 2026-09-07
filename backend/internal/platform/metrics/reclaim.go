package metrics

import "github.com/prometheus/client_golang/prometheus"

// GameReclaimCounters are the counters the game-database reclaim sweep
// reports (docs/ARCHITECTURE.md §2.4): how many participant databases and
// contest templates it removed once their contest's grace period passed, how
// many attempts of each failed, and how many instances it left for the next
// tick because something was still connected.
//
// The sweep is a background job, not an HTTP request, so it has no route or
// status code for Recorder.ObserveRequest to key on — this is what it reports
// through instead. internal/app/background.go's own task also logs these
// counts on every tick that moved anything, the same dual reporting every
// other background job in this codebase already gives its own numbers; this
// set exists for what a log line does not serve on its own — a dashboard or
// an alert asking "is the sweep actually running, or silently stuck", or
// "is something permanently busy" (Skipped climbing tick after tick, with
// Reclaimed flat, is exactly that question answering itself).
type GameReclaimCounters struct {
	reclaimed prometheus.Counter
	skipped   prometheus.Counter
	failed    prometheus.Counter

	templatesReclaimed prometheus.Counter
	templatesFailed    prometheus.Counter
}

// NewGameReclaimCounters registers the set on rec's own registry when rec is
// the Prometheus backend, and returns a counter that discards otherwise: the
// log and none backends already carry these figures through the job's own log
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

// AddInstances records one tick's outcome for participant databases. Safe to
// call regardless of backend: with no Prometheus counters behind it, it does
// nothing.
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

// AddTemplates records one tick's outcome for contest templates. Split from
// AddInstances rather than one call with four numbers: the two are different
// databases with different volumes — at most one template per contest
// against many instances — and a dashboard built on one must not have to
// filter the other out of it.
func (c *GameReclaimCounters) AddTemplates(reclaimed, failed int) {
	if c.templatesReclaimed != nil {
		c.templatesReclaimed.Add(float64(reclaimed))
	}
	if c.templatesFailed != nil {
		c.templatesFailed.Add(float64(failed))
	}
}
