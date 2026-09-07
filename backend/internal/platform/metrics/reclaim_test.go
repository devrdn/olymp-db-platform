package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The reclaim sweep is a background job, not an HTTP request, so it cannot
// report through Recorder.ObserveRequest — this is the counter pair it
// reports through instead, and this proves the numbers actually land where a
// dashboard or an alert would read them.
func TestGameReclaimCountersAccumulateOnThePrometheusBackend(t *testing.T) {
	p := NewPrometheus()
	counters := NewGameReclaimCounters(p)

	counters.Add(3, 1)
	counters.Add(2, 0)

	if got := testutil.ToFloat64(counters.reclaimed); got != 5 {
		t.Errorf("reclaimed total = %v, want 5", got)
	}
	if got := testutil.ToFloat64(counters.failed); got != 1 {
		t.Errorf("failed total = %v, want 1", got)
	}
}

// A deployment on the log or none backend has no registry to add these to —
// internal/app/background.go's own log line is where that figure goes
// instead. Add must still be safe to call unconditionally, so the reclaim
// job never needs a type switch of its own to find out which backend is
// running.
func TestGameReclaimCountersDoNothingOnNonPrometheusBackends(t *testing.T) {
	for name, rec := range allBackends(t) {
		if name == "prometheus" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			counters := NewGameReclaimCounters(rec)
			counters.Add(1, 1) // must not panic
		})
	}
}
