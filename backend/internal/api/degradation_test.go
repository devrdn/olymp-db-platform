package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
)

func TestScrapeEndpointIsServedWhenTheBackendHasOne(t *testing.T) {
	deps := testDeps()
	deps.Metrics = metrics.NewPrometheus()

	rec := do(t, NewInternalRouter(deps), http.MethodGet, "/metrics")

	if rec.Code != http.StatusOK {
		t.Errorf("/metrics = %d, want 200 with the Prometheus backend", rec.Code)
	}
}

func TestScrapeEndpointIsAbsentWhenMetricsGoToLogs(t *testing.T) {
	// Serving an empty /metrics would make a scraper believe the service is
	// instrumented when its numbers only exist in the log stream.
	deps := testDeps()
	deps.Metrics = metrics.NewLog(logging.New("info", &bytes.Buffer{}), time.Minute)

	rec := do(t, NewInternalRouter(deps), http.MethodGet, "/metrics")

	if rec.Code != http.StatusNotFound {
		t.Errorf("/metrics = %d, want 404 when metrics go to logs", rec.Code)
	}
}

func TestScrapeEndpointIsAbsentWhenMetricsAreDisabled(t *testing.T) {
	deps := testDeps()
	deps.Metrics = metrics.Noop{}

	rec := do(t, NewInternalRouter(deps), http.MethodGet, "/metrics")

	if rec.Code != http.StatusNotFound {
		t.Errorf("/metrics = %d, want 404 when metrics are off", rec.Code)
	}
}

func TestHealthEndpointsSurviveEveryMetricsBackend(t *testing.T) {
	// Metrics are diagnostics. Turning them off must not affect serving.
	backends := map[string]metrics.Recorder{
		"prometheus": metrics.NewPrometheus(),
		"log":        metrics.NewLog(logging.New("info", &bytes.Buffer{}), time.Minute),
		"none":       metrics.Noop{},
	}

	for name, rec := range backends {
		t.Run(name, func(t *testing.T) {
			deps := testDeps()
			deps.Metrics = rec
			router := NewInternalRouter(deps)

			for _, path := range []string{"/healthz", "/readyz"} {
				resp := do(t, router, http.MethodGet, path)
				if resp.Code != http.StatusOK {
					t.Errorf("%s = %d, want 200", path, resp.Code)
				}
			}
		})
	}
}

func TestReadinessReportsTheActiveCacheMode(t *testing.T) {
	// A degraded cache is invisible otherwise: the service answers normally
	// while sessions silently stop being shared between replicas.
	deps := testDeps()
	deps.CacheMode = cache.ModeMemory

	rec := do(t, NewInternalRouter(deps), http.MethodGet, "/readyz")

	var body struct {
		Cache string `json:"cache"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (raw: %s)", err, rec.Body.String())
	}
	if body.Cache != cache.ModeMemory {
		t.Errorf("cache = %q, want %q", body.Cache, cache.ModeMemory)
	}
}

func TestPublicRouterWorksWithoutAnyOptionalDependency(t *testing.T) {
	// No Prometheus, no Redis: the API still serves.
	deps := testDeps()
	deps.Metrics = metrics.Noop{}
	deps.CacheMode = cache.ModeMemory

	rec := httptest.NewRecorder()
	NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with every optional dependency absent", rec.Code)
	}
}
