package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
)

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
