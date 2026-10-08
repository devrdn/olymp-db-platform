package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func testDeps() Deps {
	return Deps{
		Logger:  logging.New("error", &bytes.Buffer{}),
		Metrics: metrics.NewPrometheus(),
		Version: "test",
	}
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestPublicRouterAnswersVersionRequest(t *testing.T) {
	rec := do(t, NewRouter(testDeps()), http.MethodGet, "/api/v1/version")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body["version"] != "test" {
		t.Errorf("version = %q, want test", body["version"])
	}
}

func TestPublicRouterDoesNotExposeMetrics(t *testing.T) {
	// Metrics reveal internal topology and traffic shape; they belong on the
	// internal listener only.
	rec := do(t, NewRouter(testDeps()), http.MethodGet, "/metrics")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d for /metrics on the public router", rec.Code, http.StatusNotFound)
	}
}

func TestPublicRouterReturnsJSONForUnknownRoute(t *testing.T) {
	rec := do(t, NewRouter(testDeps()), http.MethodGet, "/api/v1/nope")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("404 body is not JSON: %v (raw: %s)", err, rec.Body.String())
	}
	if body.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", body.Error.Code)
	}
}

func TestPublicRouterReturnsJSONForWrongMethod(t *testing.T) {
	rec := do(t, NewRouter(testDeps()), http.MethodDelete, "/api/v1/version")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
	if !strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Errorf("body = %s, want a structured method_not_allowed error", rec.Body.String())
	}
}

func TestPublicRouterSetsRequestIDHeader(t *testing.T) {
	rec := do(t, NewRouter(testDeps()), http.MethodGet, "/api/v1/version")

	if rec.Header().Get(httpx.RequestIDHeader) == "" {
		t.Error("response carries no request id header for support correlation")
	}
}

func TestPublicRouterSetsNoSniffHeader(t *testing.T) {
	rec := do(t, NewRouter(testDeps()), http.MethodGet, "/api/v1/version")

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// The header the answer key needs, asserted on the assembled router rather
// than on the middleware alone: a directive set by middleware is only worth
// anything if it survives to the response, and this is the one route shape a
// browser is free to cache heuristically — a plain 200 GET with no directive.
//
// GET /contests/{id}/export carries every reference answer of a contest, and
// GET /contests/{id}/play/log.csv is a participant's own session as a file.
// Neither is exercised here — they need a session and a database — which is
// exactly why the guarantee is a property of the router and not of a handler
// that could forget it.
func TestPublicRouterForbidsCachingEveryAnswerItGives(t *testing.T) {
	router := NewRouter(testDeps())
	router.Get("/plain", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })

	for _, path := range []string{"/api/v1/version", "/plain", "/api/v1/nothing-here"} {
		if got := do(t, router, http.MethodGet, path).Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s answered with Cache-Control %q, want no-store", path, got)
		}
	}
}

func TestPublicRouterRecoversFromHandlerPanic(t *testing.T) {
	deps := testDeps()
	router := NewRouter(deps)
	router.Get("/boom", func(w http.ResponseWriter, r *http.Request) { panic("handler exploded") })

	rec := do(t, router, http.MethodGet, "/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if strings.Contains(rec.Body.String(), "handler exploded") {
		t.Errorf("panic message leaked to the client: %s", rec.Body.String())
	}
}

func TestInternalRouterServesMetrics(t *testing.T) {
	rec := do(t, NewInternalRouter(testDeps()), http.MethodGet, "/metrics")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Errorf("metrics body looks empty: %s", rec.Body.String())
	}
}

func TestInternalRouterServesLiveness(t *testing.T) {
	rec := do(t, NewInternalRouter(testDeps()), http.MethodGet, "/healthz")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestInternalRouterServesReadiness(t *testing.T) {
	rec := do(t, NewInternalRouter(testDeps()), http.MethodGet, "/readyz")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// depsWithoutAuth is the foundation-only wiring: no authentication module
// attached. It must still produce a working router.
func depsWithoutAuth() Deps {
	return Deps{
		Logger:  logging.New("error", &bytes.Buffer{}),
		Metrics: metrics.NewPrometheus(),
		Version: "test",
	}
}

func TestRouterWorksWithoutTheAuthenticationModule(t *testing.T) {
	// The router is assembled from optional parts; a missing module must not
	// panic at startup.
	rec := do(t, NewRouter(depsWithoutAuth()), http.MethodGet, "/api/v1/version")

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestAuthRoutesAreAbsentWithoutTheModule(t *testing.T) {
	rec := do(t, NewRouter(depsWithoutAuth()), http.MethodPost, "/api/v1/auth/login")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when the module is not wired", rec.Code)
	}
}

func TestAuthRoutesAppearWhenTheModuleIsWired(t *testing.T) {
	deps := depsWithoutAuth()
	deps.Modules = []Module{moduleFunc(func(r chiRouter) {
		r.Post("/auth/login", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
	})}

	rec := do(t, NewRouter(deps), http.MethodPost, "/api/v1/auth/login")

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the wired module to serve the route", rec.Code)
	}
}

func TestMutatingRequestsAreGuardedAgainstCrossOriginWrites(t *testing.T) {
	// The session lives in a cookie, so every write needs the Origin check.
	deps := depsWithoutAuth()
	deps.Modules = []Module{moduleFunc(func(r chiRouter) {
		r.Post("/echo", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})}

	req := httptest.NewRequest(http.MethodPost, "https://contest.example/api/v1/echo", nil)
	req.Host = "contest.example"
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	NewRouter(deps).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a cross-origin write", rec.Code)
	}
}

func TestRouterResolvesClientIPThroughTrustedProxy(t *testing.T) {
	// httptest sets RemoteAddr to 192.0.2.1; trusting it as a proxy must make
	// handlers see the forwarded client, not the proxy.
	resolver, err := httpx.NewIPResolver([]string{"192.0.2.1"})
	if err != nil {
		t.Fatalf("NewIPResolver returned error: %v", err)
	}
	deps := depsWithoutAuth()
	deps.ClientIPs = resolver
	var seen string
	deps.Modules = []Module{moduleFunc(func(r chiRouter) {
		r.Get("/probe", func(w http.ResponseWriter, r *http.Request) {
			seen = httpx.ClientIP(r)
		})
	})}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	NewRouter(deps).ServeHTTP(httptest.NewRecorder(), req)

	if seen != "203.0.113.7" {
		t.Errorf("handler saw %q, want the forwarded 203.0.113.7", seen)
	}
}

func TestRouterIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	deps := depsWithoutAuth() // zero resolver: trust nobody
	var seen string
	deps.Modules = []Module{moduleFunc(func(r chiRouter) {
		r.Get("/probe", func(w http.ResponseWriter, r *http.Request) {
			seen = httpx.ClientIP(r)
		})
	})}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	req.Header.Set("X-Forwarded-For", "10.9.9.9")
	NewRouter(deps).ServeHTTP(httptest.NewRecorder(), req)

	if seen != "192.0.2.1" {
		t.Errorf("handler saw %q, want the peer 192.0.2.1 (header must be ignored)", seen)
	}
}

func TestAuditEntriesThroughTheRouterCarryTheClientAddress(t *testing.T) {
	// The finding this guards: admin actions were audited with a NULL ip
	// because nothing carried the request origin into the service layer.
	captured := &capturingSink{}
	recorder := audit.New(captured)
	deps := depsWithoutAuth()
	deps.Modules = []Module{moduleFunc(func(r chiRouter) {
		r.Get("/act", func(w http.ResponseWriter, r *http.Request) {
			_ = recorder.Record(r.Context(), audit.Entry{Action: "user.block"})
		})
	})}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/act", nil)
	req.RemoteAddr = "203.0.113.7:41000"
	req.Header.Set("User-Agent", "Admin/1.0")
	NewRouter(deps).ServeHTTP(httptest.NewRecorder(), req)

	if len(captured.entries) != 1 {
		t.Fatalf("captured %d entries, want 1", len(captured.entries))
	}
	if captured.entries[0].IP != "203.0.113.7" {
		t.Errorf("audit IP = %q, want the client address", captured.entries[0].IP)
	}
	if captured.entries[0].UserAgent != "Admin/1.0" {
		t.Errorf("audit UserAgent = %q, want the client agent", captured.entries[0].UserAgent)
	}
}

type capturingSink struct{ entries []audit.Entry }

func (c *capturingSink) Append(_ context.Context, e audit.Entry) error {
	c.entries = append(c.entries, e)
	return nil
}

func (c *capturingSink) AppendMany(_ context.Context, entries []audit.Entry) error {
	c.entries = append(c.entries, entries...)
	return nil
}

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

// A panic unwinds past anything that records after calling the next handler.
// With the recoverer outermost, a panicking request produced its own error
// line and nothing else — no access log entry, no metric, no 500 in the status
// counts. Invisible to exactly the alert it should have fired.
func TestAPanickingRequestIsStillCountedAndLogged(t *testing.T) {
	var logged bytes.Buffer
	counted := &countingRecorder{}

	deps := testDeps()
	deps.Logger = logging.New("info", &logged)
	deps.Metrics = counted
	deps.Modules = []Module{moduleFunc(func(r chi.Router) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("in a handler") })
	})}

	rec := do(t, NewRouter(deps), http.MethodGet, "/api/v1/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logged.String(), `"msg":"http request"`) {
		t.Fatal("the panicking request left no access log line")
	}
	if !strings.Contains(logged.String(), `"status":500`) {
		t.Fatalf("the access line did not record the 500: %s", logged.String())
	}
	// And the stack, without which the line says a panic happened and not
	// where.
	if !strings.Contains(logged.String(), `"stack":`) {
		t.Fatal("the panic was logged without a stack")
	}
	if counted.status != http.StatusInternalServerError {
		t.Fatalf("metrics saw status %d; a panic must be counted as the 500 it becomes", counted.status)
	}
}

// countingRecorder keeps the last observation, which is all this needs.
type countingRecorder struct{ status int }

func (c *countingRecorder) ObserveRequest(_, _ string, status int, _ time.Duration, _ bool) {
	c.status = status
}

func TestPublicRouterRefusesAQueryNoStoredTextCanMatch(t *testing.T) {
	// A NUL byte or bytes that are not UTF-8 in a query parameter would reach
	// a comparison PostgreSQL refuses by failing the statement — a 500 for
	// the client's own malformed address. Refused once, here, for every route.
	for _, target := range []string{
		"/api/v1/version?status=%00",
		"/api/v1/version?q=%FF%FE",
		"/api/v1/version?%00=x",
	} {
		rec := do(t, NewRouter(testDeps()), http.MethodGet, target)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid_request"`) {
			t.Errorf("GET %s = %d %s, want 400 invalid_request", target, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, NewRouter(testDeps()), http.MethodGet, "/api/v1/version?q=%D0%9B%25"); rec.Code != http.StatusOK {
		t.Errorf("GET with an ordinary non-ASCII query = %d, want 200", rec.Code)
	}
}
