package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
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
