package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
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
