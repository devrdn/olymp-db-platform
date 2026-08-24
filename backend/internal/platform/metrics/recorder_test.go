package metrics

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

// allBackends lists every recorder, so behaviour required of all of them is
// asserted for all of them.
func allBackends(t *testing.T) map[string]Recorder {
	t.Helper()
	return map[string]Recorder{
		"prometheus": NewPrometheus(),
		"log":        NewLog(logging.New("info", &bytes.Buffer{}), time.Minute),
		"none":       Noop{},
	}
}

func TestEveryBackendAcceptsObservationsWithoutFailing(t *testing.T) {
	// The service records a metric on every request. No backend may panic or
	// need special handling at the call site.
	for name, rec := range allBackends(t) {
		t.Run(name, func(t *testing.T) {
			rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 5*time.Millisecond)
			rec.ObserveRequest(http.MethodPost, "/api/v1/contests", 500, time.Second)
		})
	}
}

func TestOnlyPrometheusExposesAScrapeEndpoint(t *testing.T) {
	// The internal router registers /metrics only when the backend has one,
	// so a log-only deployment answers 404 instead of an empty page.
	if _, ok := any(NewPrometheus()).(Scraper); !ok {
		t.Error("prometheus backend does not expose a scrape endpoint")
	}
	if _, ok := any(NewLog(logging.New("info", &bytes.Buffer{}), time.Minute)).(Scraper); ok {
		t.Error("log backend exposes a scrape endpoint; it reports through logs")
	}
	if _, ok := any(Noop{}).(Scraper); ok {
		t.Error("noop backend exposes a scrape endpoint")
	}
}

func TestLogBackendReportsAggregatedCounts(t *testing.T) {
	var buf bytes.Buffer
	rec := NewLog(logging.New("info", &buf), time.Minute)

	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 10*time.Millisecond)
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 30*time.Millisecond)
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 500, 20*time.Millisecond)
	rec.Flush()

	// One record per method+route+status group, not one per request: the
	// access log already carries individual requests.
	records := decodeRecords(t, &buf)
	var ok200, ok500 map[string]any
	for _, r := range records {
		if r["route"] != "/api/v1/version" {
			continue
		}
		switch r["status"] {
		case float64(200):
			ok200 = r
		case float64(500):
			ok500 = r
		}
	}

	if ok200 == nil {
		t.Fatalf("no aggregate record for status 200: %s", buf.String())
	}
	if ok200["count"] != float64(2) {
		t.Errorf("count for 200 = %v, want 2", ok200["count"])
	}
	if ok500 == nil || ok500["count"] != float64(1) {
		t.Errorf("aggregate record for status 500 missing or wrong: %v", ok500)
	}
}

func TestLogBackendReportsLatency(t *testing.T) {
	var buf bytes.Buffer
	rec := NewLog(logging.New("info", &buf), time.Minute)

	rec.ObserveRequest(http.MethodGet, "/slow", 200, 100*time.Millisecond)
	rec.ObserveRequest(http.MethodGet, "/slow", 200, 300*time.Millisecond)
	rec.Flush()

	r := decodeRecords(t, &buf)[0]
	if r["max_ms"] != float64(300) {
		t.Errorf("max_ms = %v, want 300", r["max_ms"])
	}
	if r["avg_ms"] != float64(200) {
		t.Errorf("avg_ms = %v, want 200", r["avg_ms"])
	}
}

func TestLogBackendResetsCountersAfterFlush(t *testing.T) {
	// Counters must not accumulate across intervals, or every report would
	// restate the whole history.
	var buf bytes.Buffer
	rec := NewLog(logging.New("info", &buf), time.Minute)
	rec.ObserveRequest(http.MethodGet, "/x", 200, time.Millisecond)
	rec.Flush()
	buf.Reset()

	rec.Flush()

	if buf.Len() != 0 {
		t.Errorf("second flush reported stale data: %s", buf.String())
	}
}

func TestNewSelectsBackendByName(t *testing.T) {
	log := logging.New("info", &bytes.Buffer{})

	cases := map[string]string{
		"prometheus": "*metrics.Prometheus",
		"log":        "*metrics.Log",
		"none":       "metrics.Noop",
	}
	for name := range cases {
		rec, err := New(name, log)
		if err != nil {
			t.Errorf("New(%q) returned error: %v", name, err)
			continue
		}
		if rec == nil {
			t.Errorf("New(%q) returned nil recorder", name)
		}
	}
}

func TestNewRejectsUnknownBackend(t *testing.T) {
	_, err := New("statsd", logging.New("info", &bytes.Buffer{}))

	if err == nil {
		t.Fatal("New() accepted an unknown backend, want error")
	}
	if !strings.Contains(err.Error(), "statsd") {
		t.Errorf("error %q does not name the offending value", err)
	}
}

func TestMiddlewareWorksWithEveryBackend(t *testing.T) {
	for name, rec := range allBackends(t) {
		t.Run(name, func(t *testing.T) {
			handler := Middleware(rec)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))

			if resp.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.Code)
			}
		})
	}
}

// decodeRecords parses every JSON line written to buf.
func decodeRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %v (raw: %s)", err, line)
		}
		out = append(out, rec)
	}
	return out
}
