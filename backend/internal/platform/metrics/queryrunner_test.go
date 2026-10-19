package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// runnerEndings is a small stand-in for queryrunner.Endings().
var runnerEndings = map[string][]string{
	"ok":       {""},
	"rejected": {"busy", "parse_error"},
	"timeout":  {"queue", "deadline"},
	"error":    {"database"},
}

var runnerCutOffs = []string{"rows", "bytes"}

func TestQueryRunnerCountsEachEndingUnderItsOutcomeAndReason(t *testing.T) {
	p := NewPrometheus()
	m := NewQueryRunner(p, runnerEndings, runnerCutOffs)

	m.Ended("ok", "")
	m.Ended("ok", "")
	m.Ended("rejected", "parse_error")
	m.Ended("timeout", "queue")

	for _, tc := range []struct {
		outcome, reason string
		want            float64
	}{
		{"ok", "", 2},
		{"rejected", "parse_error", 1},
		{"rejected", "busy", 0},
		{"timeout", "queue", 1},
	} {
		if got := testutil.ToFloat64(m.queries.WithLabelValues(tc.outcome, tc.reason)); got != tc.want {
			t.Errorf("queries{outcome=%q,reason=%q} = %v, want %v", tc.outcome, tc.reason, got, tc.want)
		}
	}
}

// A series that exists only after its first increment makes rate() blind to
// that increment, so every declared ending is exported at zero from the start.
func TestQueryRunnerExportsEveryDeclaredSeriesBeforeItHappens(t *testing.T) {
	p := NewPrometheus()
	NewQueryRunner(p, runnerEndings, runnerCutOffs)

	body := scrape(t, p)
	for _, want := range []string{
		`queryrunner_queries_total{outcome="rejected",reason="busy"} 0`,
		`queryrunner_queries_total{outcome="timeout",reason="deadline"} 0`,
		`queryrunner_result_cutoffs_total{limit="bytes"} 0`,
		`queryrunner_query_duration_seconds_count{outcome="error"} 0`,
		`queryrunner_admission_wait_seconds_count 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %s", want)
		}
	}
}

// Labels are bounded by the lists the runner declared: a value outside them,
// whatever produced it, is folded into "other" rather than minting a series.
func TestQueryRunnerFoldsUndeclaredLabelValuesIntoOther(t *testing.T) {
	p := NewPrometheus()
	m := NewQueryRunner(p, runnerEndings, runnerCutOffs)

	m.Ended("rejected", "SELECT * FROM secrets")
	m.Ended("exploded", "busy")
	m.Executed("exploded", time.Second)
	m.CutOff("participant-42")

	if got := testutil.ToFloat64(m.queries.WithLabelValues("rejected", otherLabel)); got != 1 {
		t.Errorf("undeclared reason: queries{rejected,other} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.queries.WithLabelValues(otherLabel, otherLabel)); got != 1 {
		t.Errorf("undeclared outcome: queries{other,other} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.cutoffs.WithLabelValues(otherLabel)); got != 1 {
		t.Errorf("undeclared limit: cutoffs{other} = %v, want 1", got)
	}

	body := scrape(t, p)
	for _, leaked := range []string{"secrets", "exploded", "participant-42"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the scrape carries the undeclared value %q", leaked)
		}
	}
}

func TestQueryRunnerObservesWaitsDurationsAndCutOffs(t *testing.T) {
	p := NewPrometheus()
	m := NewQueryRunner(p, runnerEndings, runnerCutOffs)

	m.Waited(0)
	m.Waited(2 * time.Second)
	m.Executed("ok", 30*time.Millisecond)
	m.Executed("timeout", 5*time.Second)
	m.CutOff("rows")
	m.CutOff("rows")

	body := scrape(t, p)
	for _, want := range []string{
		`queryrunner_admission_wait_seconds_count 2`,
		`queryrunner_admission_wait_seconds_sum 2`,
		`queryrunner_query_duration_seconds_count{outcome="ok"} 1`,
		`queryrunner_query_duration_seconds_count{outcome="timeout"} 1`,
		`queryrunner_result_cutoffs_total{limit="rows"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %s", want)
		}
	}
}

// Running and waiting are read when scraped, so they cannot drift from the
// gate and cost nothing per query.
func TestQueryRunnerReadsAdmissionWhenScraped(t *testing.T) {
	p := NewPrometheus()
	m := NewQueryRunner(p, runnerEndings, runnerCutOffs)

	running, waiting := 3, 7
	m.WatchAdmission(func() (int, int) { return running, waiting })

	body := scrape(t, p)
	for _, want := range []string{"queryrunner_active_queries 3", "queryrunner_admission_queue_depth 7"} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape is missing %s", want)
		}
	}

	running, waiting = 0, 0
	body = scrape(t, p)
	for _, want := range []string{"queryrunner_active_queries 0", "queryrunner_admission_queue_depth 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("after the queue drained, scrape is missing %s", want)
		}
	}
}

func TestQueryRunnerDoesNothingOnNonPrometheusBackends(t *testing.T) {
	for name, rec := range allBackends(t) {
		if name == "prometheus" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			m := NewQueryRunner(rec, runnerEndings, runnerCutOffs)
			// None of these may panic.
			m.Ended("ok", "")
			m.Waited(time.Second)
			m.Executed("ok", time.Second)
			m.CutOff("rows")
			m.WatchAdmission(func() (int, int) { return 1, 1 })
		})
	}
}
