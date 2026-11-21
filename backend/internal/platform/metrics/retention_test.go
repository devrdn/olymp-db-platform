package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var retentionTables = []string{"audit_log", "mail_outbox", "query_log"}

func TestRetentionCountersStartAtZeroForEveryTable(t *testing.T) {
	p := NewPrometheus()
	NewRetentionCounters(p, retentionTables)

	want := `
# HELP retention_failures_total Retention passes over a table that failed, by table; the rows stay until a later pass succeeds.
# TYPE retention_failures_total counter
retention_failures_total{table="audit_log"} 0
retention_failures_total{table="mail_outbox"} 0
retention_failures_total{table="query_log"} 0
# HELP retention_rows_deleted_total Rows the retention sweep deleted for being past their period, by table.
# TYPE retention_rows_deleted_total counter
retention_rows_deleted_total{table="audit_log"} 0
retention_rows_deleted_total{table="mail_outbox"} 0
retention_rows_deleted_total{table="query_log"} 0
`
	if err := testutil.GatherAndCompare(p.Registry(), strings.NewReader(want), "retention_rows_deleted_total", "retention_failures_total"); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionCountersAccumulate(t *testing.T) {
	p := NewPrometheus()
	c := NewRetentionCounters(p, retentionTables)

	c.Add("query_log", 5000, false)
	c.Add("query_log", 12, true)

	if got := testutil.ToFloat64(c.deleted.WithLabelValues("query_log")); got != 5012 {
		t.Errorf("deleted = %v, want 5012", got)
	}
	if got := testutil.ToFloat64(c.failures.WithLabelValues("query_log")); got != 1 {
		t.Errorf("failures = %v, want 1", got)
	}
}

// A table outside the declared set would be a new series per value, so it is
// dropped rather than counted.
func TestRetentionCountersIgnoreATableTheyWereNotGiven(t *testing.T) {
	p := NewPrometheus()
	c := NewRetentionCounters(p, retentionTables)

	c.Add("users", 1, true)

	if n := testutil.CollectAndCount(c.deleted); n != 3 {
		t.Errorf("retention_rows_deleted_total has %d series, want the 3 declared ones", n)
	}
	if n := testutil.CollectAndCount(c.failures); n != 3 {
		t.Errorf("retention_failures_total has %d series, want the 3 declared ones", n)
	}
}

func TestRetentionCountersDoNothingOnNonPrometheusBackends(t *testing.T) {
	t.Run("log", func(t *testing.T) {
		var buf bytes.Buffer
		rec := NewLog(logging.New("info", &buf), time.Minute)
		NewRetentionCounters(rec, retentionTables).Add("query_log", 5, true)
		rec.Flush()

		if strings.Contains(buf.String(), "retention_") {
			t.Errorf("the log digest mentions retention counters: %q", buf.String())
		}
	})
}
