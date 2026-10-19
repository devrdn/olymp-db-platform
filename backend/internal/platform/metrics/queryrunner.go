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

	// The declared series, resolved once so a query costs a map lookup and
	// an atomic add. A value missing here goes to "other" through the vec.
	ended    map[string]map[string]prometheus.Counter
	executed map[string]prometheus.Observer
	cut      map[string]prometheus.Counter
}

// NewQueryRunner registers the set on rec's registry when rec is the
// Prometheus backend, and returns a set that discards otherwise. endings maps
// each outcome to the reasons it may carry; cutoffs lists the budget limits
// that may cut an answer. Every declared series is exported at zero from the
// start, so rate() sees its first increment.
func NewQueryRunner(rec Recorder, endings map[string][]string, cutoffs []string) *QueryRunner {
	q := &QueryRunner{}
	p, ok := rec.(*Prometheus)
	if !ok {
		return q
	}

	q.registry = p.Registry()
	q.queries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "queryrunner_queries_total",
		Help: "Queries the Query Runner answered, by outcome and reason.",
	}, []string{"outcome", "reason"})
	// From a fast read to past the deadline, where a cancelled query's
	// cleanup lands. A QUERY_DEADLINE above the top bucket lands in +Inf.
	q.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "queryrunner_query_duration_seconds",
		Help:    "Time from admission to the end of an admitted query, connecting included, by outcome.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"outcome"})
	// From a query admitted at once to one that waited out the queue. A
	// QUERY_QUEUE_WAIT above the top bucket lands in +Inf.
	q.wait = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "queryrunner_admission_wait_seconds",
		Help:    "Time a query waited for a running slot, whether it got one, timed out or was abandoned.",
		Buckets: []float64{.001, .01, .05, .1, .25, .5, 1, 2.5, 5, 10, 15, 30},
	})
	q.cutoffs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "queryrunner_result_cutoffs_total",
		Help: "Answers cut to the result budget, by the limit that cut them.",
	}, []string{"limit"})
	q.registry.MustRegister(q.queries, q.duration, q.wait, q.cutoffs)

	q.ended = make(map[string]map[string]prometheus.Counter, len(endings))
	q.executed = make(map[string]prometheus.Observer, len(endings))
	for outcome, reasons := range endings {
		q.executed[outcome] = q.duration.WithLabelValues(outcome)
		q.ended[outcome] = make(map[string]prometheus.Counter, len(reasons))
		for _, reason := range reasons {
			q.ended[outcome][reason] = q.queries.WithLabelValues(outcome, reason)
		}
	}
	q.cut = make(map[string]prometheus.Counter, len(cutoffs))
	for _, limit := range cutoffs {
		q.cut[limit] = q.cutoffs.WithLabelValues(limit)
	}
	return q
}

// Ended counts one query's ending.
func (q *QueryRunner) Ended(outcome, reason string) {
	if q.queries == nil {
		return
	}
	reasons, known := q.ended[outcome]
	if !known {
		q.queries.WithLabelValues(otherLabel, otherLabel).Inc()
		return
	}
	if counter, known := reasons[reason]; known {
		counter.Inc()
		return
	}
	q.queries.WithLabelValues(outcome, otherLabel).Inc()
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
	if observer, known := q.executed[outcome]; known {
		observer.Observe(d.Seconds())
		return
	}
	q.duration.WithLabelValues(otherLabel).Observe(d.Seconds())
}

// CutOff counts one answer cut by limit.
func (q *QueryRunner) CutOff(limit string) {
	if q.cutoffs == nil {
		return
	}
	if counter, known := q.cut[limit]; known {
		counter.Inc()
		return
	}
	q.cutoffs.WithLabelValues(otherLabel).Inc()
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
