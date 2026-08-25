package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubChecker reports a fixed outcome for a named dependency.
type stubChecker struct {
	name string
	err  error
}

func (s stubChecker) Name() string                  { return s.name }
func (s stubChecker) Check(_ context.Context) error { return s.err }

// startStack starts the public and internal listeners the way main.go does and
// returns their base URLs.
func startStack(t *testing.T, checkers ...health.Checker) (publicURL, internalURL string) {
	t.Helper()

	log := logging.New("error", io.Discard)
	deps := api.Deps{
		Logger:   log,
		Metrics:  metrics.NewPrometheus(),
		Version:  "integration",
		Checkers: checkers,
	}

	public := server.New("public", "127.0.0.1:0", api.NewRouter(deps), log)
	internal := server.New("internal", "127.0.0.1:0", api.NewInternalRouter(deps), log)

	if err := public.Start(); err != nil {
		t.Fatalf("start public listener: %v", err)
	}
	if err := internal.Start(); err != nil {
		t.Fatalf("start internal listener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = internal.Shutdown(ctx)
	})

	return "http://" + public.Addr(), "http://" + internal.Addr()
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestAssembledServiceServesVersionOnThePublicListener(t *testing.T) {
	publicURL, _ := startStack(t)

	status, body := get(t, publicURL+"/api/v1/version")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if payload["version"] != "integration" {
		t.Errorf("version = %q, want integration", payload["version"])
	}
}

func TestAssembledServiceKeepsOperationalEndpointsOffThePublicListener(t *testing.T) {
	// This separation is the reason for two listeners: the published port must
	// not answer for metrics or readiness.
	publicURL, _ := startStack(t)

	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		status, _ := get(t, publicURL+path)
		if status != http.StatusNotFound {
			t.Errorf("public %s = %d, want 404", path, status)
		}
	}
}

func TestAssembledServiceServesOperationalEndpointsOnTheInternalListener(t *testing.T) {
	_, internalURL := startStack(t, stubChecker{name: "core-db"}, stubChecker{name: "redis"})

	for _, path := range []string{"/metrics", "/healthz", "/readyz"} {
		status, body := get(t, internalURL+path)
		if status != http.StatusOK {
			t.Errorf("internal %s = %d, want 200 (body: %s)", path, status, body)
		}
	}
}

func TestReadinessTurnsRedWhenADependencyIsDown(t *testing.T) {
	_, internalURL := startStack(t,
		stubChecker{name: "core-db", err: errors.New("connection refused")},
		stubChecker{name: "redis"},
	)

	status, body := get(t, internalURL+"/readyz")

	if status != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want 503 (body: %s)", status, body)
	}
}

func TestLivenessStaysGreenWhenADependencyIsDown(t *testing.T) {
	// Restarting the process cannot fix a broken database, so liveness must not
	// follow readiness down.
	_, internalURL := startStack(t, stubChecker{name: "core-db", err: errors.New("connection refused")})

	status, _ := get(t, internalURL+"/healthz")

	if status != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 while a dependency is down", status)
	}
}

func TestSelfHealthcheckProbeSucceedsAgainstTheInternalListener(t *testing.T) {
	// This is what `api -healthcheck` runs inside the container.
	_, internalURL := startStack(t)

	if err := health.Probe(context.Background(), internalURL+"/healthz"); err != nil {
		t.Errorf("Probe() = %v, want nil", err)
	}
}

func TestRequestsAreCountedInTheMetricsExposition(t *testing.T) {
	publicURL, internalURL := startStack(t)

	get(t, publicURL+"/api/v1/version")

	_, body := get(t, internalURL+"/metrics")
	if !strings.Contains(body, `route="/api/v1/version"`) {
		t.Errorf("metrics do not record the served route:\n%s", body)
	}
}

// startDegradedStack assembles the service the way main.go would with no Redis
// and no Prometheus, and returns the two base URLs plus the startup log.
func startDegradedStack(t *testing.T) (publicURL, internalURL string, startupLog *bytes.Buffer) {
	t.Helper()

	buf := &bytes.Buffer{}
	log := logging.New("info", buf)

	cacheBackend, err := cache.New(context.Background(), "", log)
	if err != nil {
		t.Fatalf("cache.New with no address returned error: %v", err)
	}
	t.Cleanup(func() { _ = cacheBackend.Close() })

	recorder, err := metrics.New(metrics.BackendLog, log)
	if err != nil {
		t.Fatalf("metrics.New(log) returned error: %v", err)
	}

	deps := api.Deps{
		Logger:    log,
		Metrics:   recorder,
		Version:   "degraded",
		CacheMode: cache.Mode(cacheBackend),
		Checkers:  []health.Checker{cacheChecker{cacheBackend}},
	}

	public := server.New("public", "127.0.0.1:0", api.NewRouter(deps), log)
	internal := server.New("internal", "127.0.0.1:0", api.NewInternalRouter(deps), log)
	if err := public.Start(); err != nil {
		t.Fatalf("start public listener: %v", err)
	}
	if err := internal.Start(); err != nil {
		t.Fatalf("start internal listener: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = public.Shutdown(ctx)
		_ = internal.Shutdown(ctx)
	})

	return "http://" + public.Addr(), "http://" + internal.Addr(), buf
}

// cacheChecker adapts the cache to the readiness probe.
type cacheChecker struct{ c cache.Cache }

func (c cacheChecker) Name() string                    { return "cache" }
func (c cacheChecker) Check(ctx context.Context) error { return c.c.Ping(ctx) }

func TestServiceServesWithNeitherRedisNorPrometheus(t *testing.T) {
	publicURL, _, _ := startDegradedStack(t)

	status, body := get(t, publicURL+"/api/v1/version")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
}

func TestDegradedServiceReportsItselfReady(t *testing.T) {
	// The in-process cache is a working backend, not a failure: readiness must
	// be green so the instance receives traffic.
	_, internalURL, _ := startDegradedStack(t)

	status, body := get(t, internalURL+"/readyz")

	if status != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200 (body: %s)", status, body)
	}
	var payload struct {
		Cache string `json:"cache"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if payload.Cache != cache.ModeMemory {
		t.Errorf("cache = %q, want %q so the degradation is visible", payload.Cache, cache.ModeMemory)
	}
}

func TestDegradedServiceHasNoScrapeEndpoint(t *testing.T) {
	_, internalURL, _ := startDegradedStack(t)

	status, _ := get(t, internalURL+"/metrics")

	if status != http.StatusNotFound {
		t.Errorf("/metrics = %d, want 404 when no Prometheus backend is active", status)
	}
}

func TestDegradedStartupWarnsAboutTheCacheFallback(t *testing.T) {
	_, _, startupLog := startDegradedStack(t)

	out := startupLog.String()
	if !strings.Contains(out, "WARN") {
		t.Errorf("startup did not warn about the in-process cache: %s", out)
	}
}

func TestDegradedServiceStillMeasuresTraffic(t *testing.T) {
	// Metrics move to the log stream; they do not disappear.
	publicURL, _, startupLog := startDegradedStack(t)
	get(t, publicURL+"/api/v1/version")
	startupLog.Reset()

	// Force the digest instead of waiting for the interval.
	rec, err := metrics.New(metrics.BackendLog, logging.New("info", startupLog))
	if err != nil {
		t.Fatalf("metrics.New returned error: %v", err)
	}
	rec.ObserveRequest(http.MethodGet, "/api/v1/version", 200, 3*time.Millisecond)
	rec.(*metrics.Log).Flush()

	if !strings.Contains(startupLog.String(), "http metrics") {
		t.Errorf("log backend produced no metric record: %s", startupLog.String())
	}
}
