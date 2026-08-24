package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
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
