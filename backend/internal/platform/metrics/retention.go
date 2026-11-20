package metrics

import "github.com/prometheus/client_golang/prometheus"

// RetentionCounters are what the retention sweep reports: the rows it deleted
// and the passes over a table that failed, by table.
type RetentionCounters struct {
	deleted  *prometheus.CounterVec
	failures *prometheus.CounterVec
	declared map[string]bool
}

// NewRetentionCounters registers the set on rec's registry when rec is the
// Prometheus backend, and returns a set that discards otherwise. Every table
// is exported at zero from the start, so increase() sees the first failure.
func NewRetentionCounters(rec Recorder, tables []string) *RetentionCounters {
	p, ok := rec.(*Prometheus)
	if !ok {
		return &RetentionCounters{}
	}

	c := &RetentionCounters{
		deleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "retention_rows_deleted_total",
			Help: "Rows the retention sweep deleted for being past their period, by table.",
		}, []string{"table"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "retention_failures_total",
			Help: "Retention passes over a table that failed, by table; the rows stay until a later pass succeeds.",
		}, []string{"table"}),
		declared: map[string]bool{},
	}
	for _, table := range tables {
		c.deleted.WithLabelValues(table)
		c.failures.WithLabelValues(table)
		c.declared[table] = true
	}
	p.Registry().MustRegister(c.deleted, c.failures)
	return c
}

// Add records one pass over table: the rows it deleted, even when it failed
// part way, and whether it failed. An undeclared table is dropped, so a
// caller's mistake cannot grow the series without bound.
func (c *RetentionCounters) Add(table string, deleted int, failed bool) {
	if c.deleted == nil || !c.declared[table] {
		return
	}
	c.deleted.WithLabelValues(table).Add(float64(deleted))
	if failed {
		c.failures.WithLabelValues(table).Inc()
	}
}
