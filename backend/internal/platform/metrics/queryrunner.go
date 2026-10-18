package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// otherLabel stands in for any label value outside the declared lists. Seeing
// it on a dashboard means the runner reported something it never declared.
const otherLabel = "other"

// QueryRunner is the Query Runner's own set: how each query ended, how long
// admission and execution took, how many queries run and wait, and how often
// an answer was cut to its budget.
//
// Label values are bounded by the lists given to NewQueryRunner, never by
// what a request carries: an undeclared value is counted as "other", so no
// participant's SQL, database or identity can become a series.
type QueryRunner struct {
	registry *prometheus.Registry

	queries  *prometheus.CounterVec
	duration *prometheus.HistogramVec
	wait     prometheus.Histogram
	cutoffs  *prometheus.CounterVec

	endings map[string]map[string]struct{}
	limits  map[string]struct{}
}

// NewQueryRunner registers the set on rec's registry when rec is the
// Prometheus backend, and returns a set that discards otherwise. endings maps
// each outcome to the reasons it may carry; cutoffs lists the budget limits
// that may cut an answer. Every declared series is exported at zero from the
// start, so rate() sees its first increment.
func NewQueryRunner(rec Recorder, endings map[string][]string, cutoffs []string) *QueryRunner {
	q := &QueryRunner{
		endings: make(map[string]map[string]struct{}, len(endings)),
		limits:  make(map[string]struct{}, len(cutoffs)),
	}
	for outcome, reasons := range endings {
		q.endings[outcome] = make(map[string]struct{}, len(reasons))
		for _, reason := range reasons {
			q.endings[outcome][reason] = struct{}{}
		}
	}
	for _, limit := range cutoffs {
		q.limits[limit] = struct{}{}
	}

	p, ok := rec.(*Prometheus)
	if !ok {
		return q
	}

	q.registry = p.Registry()
	q.queries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "queryrunner_queries_total",
		Help: "Queries the Query Runner answered, by outcome (ok, rejected, error, timeout) and reason.",
	}, []string{"outcome", "reason"})
	// Up to the deadline (5s by default) and a little past it, where a
	// cancelled query's cleanup lands.
	q.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "queryrunner_query_duration_seconds",
		Help:    "Time from admission to the end of an admitted query, connecting included, by outcome.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"outcome"})
	// Zero for a query admitted at once, up to the queue wait (15s by
	// default) for one that timed out waiting.
	q.wait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "queryrunner_admission_wait_seconds",
		Help:    "Time a query waited for a running slot, whether it got one or gave up.",
		Buckets: []float64{.001, .01, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 30},
	})
	q.cutoffs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "queryrunner_result_cutoffs_total",
		Help: "Answers cut to the result budget, by the limit that cut them.",
	}, []string{"limit"})
	q.registry.MustRegister(q.queries, q.duration, q.wait, q.cutoffs)

	for outcome, reasons := range q.endings {
		q.duration.WithLabelValues(outcome)
		for reason := range reasons {
			q.queries.WithLabelValues(outcome, reason)
		}
	}
	for limit := range q.limits {
		q.cutoffs.WithLabelValues(limit)
	}
	return q
}

// Ended counts one query's ending.
func (q *QueryRunner) Ended(outcome, reason string) {
	if q.queries == nil {
		return
	}
	reasons, known := q.endings[outcome]
	if !known {
		q.queries.WithLabelValues(otherLabel, otherLabel).Inc()
		return
	}
	if _, known := reasons[reason]; !known {
		reason = otherLabel
	}
	q.queries.WithLabelValues(outcome, reason).Inc()
}

// Waited records how long one query waited for admission.
func (q *QueryRunner) Waited(d time.Duration) {
	if q.wait == nil {
		return
	}
	q.wait.Observe(d.Seconds())
}

// Executed records how long one admitted query took.
func (q *QueryRunner) Executed(outcome string, d time.Duration) {
	if q.duration == nil {
		return
	}
	if _, known := q.endings[outcome]; !known {
		outcome = otherLabel
	}
	q.duration.WithLabelValues(outcome).Observe(d.Seconds())
}

// CutOff counts one answer cut by limit.
func (q *QueryRunner) CutOff(limit string) {
	if q.cutoffs == nil {
		return
	}
	if _, known := q.limits[limit]; !known {
		limit = otherLabel
	}
	q.cutoffs.WithLabelValues(limit).Inc()
}

// WatchAdmission exports how many queries run and wait, read from load at
// scrape time rather than updated per query. load must be safe for concurrent
// use; call WatchAdmission once.
func (q *QueryRunner) WatchAdmission(load func() (running, waiting int)) {
	if q.registry == nil {
		return
	}
	q.registry.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "queryrunner_active_queries",
			Help: "Queries holding a running slot.",
		}, func() float64 {
			running, _ := load()
			return float64(running)
		}),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "queryrunner_admission_queue_depth",
			Help: "Queries waiting for a running slot.",
		}, func() float64 {
			_, waiting := load()
			return float64(waiting)
		}),
	)
}
