package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
)

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
