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

func allBackends(t *testing.T) map[string]Recorder {
	t.Helper()
	return map[string]Recorder{
		"prometheus": NewPrometheus(),
		"log":        NewLog(logging.New("info", &bytes.Buffer{}), time.Minute),
		"none":       Noop{},
	}
}

func TestEveryBackendAcceptsObservationsWithoutFailing(t *testing.T) {
	for name, rec := range allBackends(t) {
		t.Run(name, func(t *testing.T) {
			rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 5*time.Millisecond, false)
			rec.ObserveRequest(http.MethodPost, "/api/v1/contests", 500, time.Second, false)
			rec.ObserveRequest(http.MethodGet, "/api/v1/contests/{contestID}/events", 200, time.Hour, true)
		})
	}
}

func TestOnlyPrometheusExposesAScrapeEndpoint(t *testing.T) {
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

	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 10*time.Millisecond, false)
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 30*time.Millisecond, false)
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 500, 20*time.Millisecond, false)
	rec.Flush()

	// One record per method+route+status group, not one per request.
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

	rec.ObserveRequest(http.MethodGet, "/slow", 200, 100*time.Millisecond, false)
	rec.ObserveRequest(http.MethodGet, "/slow", 200, 300*time.Millisecond, false)
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
	var buf bytes.Buffer
	rec := NewLog(logging.New("info", &buf), time.Minute)
	rec.ObserveRequest(http.MethodGet, "/x", 200, time.Millisecond, false)
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

func TestPrometheusRoutesAStreamingObservationToItsOwnHistogram(t *testing.T) {
	p := NewPrometheus()
	p.ObserveRequest(http.MethodGet, "/contests/{contestID}/events", 200, time.Hour, true)
	p.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 5*time.Millisecond, false)

	rec := httptest.NewRecorder()
	p.ScrapeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `http_stream_duration_seconds_count{method="GET",route="/contests/{contestID}/events"} 1`) {
		t.Errorf("the streaming observation did not reach http_stream_duration_seconds: %s", body)
	}
	if strings.Contains(body, `http_request_duration_seconds_count{method="GET",route="/contests/{contestID}/events"}`) {
		t.Errorf("the streaming observation also landed in the shared request-duration histogram: %s", body)
	}
	if !strings.Contains(body, `http_requests_total{method="GET",route="/api/v1/version",status="200"} 1`) {
		t.Errorf("an ordinary request was not counted: %s", body)
	}
}

func TestPrometheusLeavesOrdinaryRequestsOutOfTheStreamHistogram(t *testing.T) {
	p := NewPrometheus()
	p.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 5*time.Millisecond, false)

	rec := httptest.NewRecorder()
	p.ScrapeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if strings.Contains(rec.Body.String(), "http_stream_duration_seconds_count{") {
		t.Errorf("an ordinary request produced a series in the stream histogram: %s", rec.Body.String())
	}
}

func TestLogBackendReportsStreamingSeparatelyFromOrdinaryRequests(t *testing.T) {
	var buf bytes.Buffer
	rec := NewLog(logging.New("info", &buf), time.Minute)

	rec.ObserveRequest(http.MethodGet, "/contests/{contestID}/events", 200, time.Hour, true)
	rec.ObserveRequest(http.MethodGet, "/contests/{contestID}/events", 200, 5*time.Millisecond, false)
	rec.Flush()

	var streaming, ordinary map[string]any
	for _, r := range decodeRecords(t, &buf) {
		if r["route"] != "/contests/{contestID}/events" {
			continue
		}
		if r["streaming"] == true {
			streaming = r
		} else {
			ordinary = r
		}
	}
	if streaming == nil || ordinary == nil {
		t.Fatalf("want one streaming and one ordinary aggregate for the same route, got: %s", buf.String())
	}
	if streaming["count"] != float64(1) || ordinary["count"] != float64(1) {
		t.Errorf("streaming = %v, ordinary = %v, want one request folded into each, not blended together", streaming, ordinary)
	}
}

func TestMarkStreamingReachesTheRecorderThroughMiddleware(t *testing.T) {
	p := NewPrometheus()
	handler := Middleware(p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		MarkStreaming(r)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/contests/{contestID}/events", nil))

	rec := httptest.NewRecorder()
	p.ScrapeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `http_stream_duration_seconds_count{method="GET",route="unknown"} 1`) {
		t.Errorf("MarkStreaming did not route the observation to the stream histogram: %s", rec.Body.String())
	}
}

func TestMiddlewareDefaultsToTheSharedHistogramWithoutMarkStreaming(t *testing.T) {
	p := NewPrometheus()
	handler := Middleware(p)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	rec := httptest.NewRecorder()
	p.ScrapeHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "http_stream_duration_seconds_count{") {
		t.Errorf("an unmarked handler's response reached the stream histogram: %s", rec.Body.String())
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
