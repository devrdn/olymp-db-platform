// Package api assembles the HTTP surface of the Core API.
//
// Two routers are built from the same dependencies. The public router serves
// the participant and administrator API and is the only one published through
// the reverse proxy. The internal router serves operational endpoints
// (metrics, liveness, readiness) and stays on a port that is never exposed.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/go-chi/chi/v5"
)

// defaultReadinessTimeout bounds the dependency probes so the endpoint always
// answers, even when a dependency hangs instead of refusing.
const defaultReadinessTimeout = 3 * time.Second

// Deps carries everything the routers need. Handlers receive their
// collaborators explicitly rather than reaching for package-level state.
type Deps struct {
	Logger *slog.Logger
	// Metrics is any recorder; the router adapts to what the backend offers.
	Metrics  metrics.Recorder
	Version  string
	Checkers []health.Checker
	// ClientIPs resolves the real client address behind the reverse proxy.
	// The zero value trusts nobody, which is the safe default: forwarded
	// headers are then ignored and the TCP peer is the client.
	ClientIPs httpx.IPResolver
	// CacheMode names the active cache backend, reported by readiness so a
	// degraded install is visible to operators.
	CacheMode        string
	ReadinessTimeout time.Duration
	// Modules contribute the routes of a feature area under /api/v1. The
	// router knows nothing about what they serve, so a feature is added by
	// wiring one in main rather than by editing this package.
	Modules []Module
}

// Module registers the routes of one feature area.
type Module interface {
	Mount(r chi.Router)
}

// chiRouter is the router type a module receives.
type chiRouter = chi.Router

// moduleFunc adapts a function to Module.
type moduleFunc func(r chi.Router)

func (f moduleFunc) Mount(r chi.Router) { f(r) }

func (d Deps) readinessTimeout() time.Duration {
	if d.ReadinessTimeout > 0 {
		return d.ReadinessTimeout
	}
	return defaultReadinessTimeout
}

// recorder never returns nil, so an incompletely built Deps degrades to "no
// metrics" instead of panicking on the first request.
func (d Deps) recorder() metrics.Recorder {
	if d.Metrics == nil {
		return metrics.Noop{}
	}
	return d.Metrics
}

// NewRouter builds the public API router.
func NewRouter(deps Deps) *chi.Mux {
	r := chi.NewRouter()

	// Order matters: request identity first so every later record and error
	// body can carry it, recovery next so a panic in any handler below still
	// produces a logged 500, then observability, then response hardening.
	r.Use(httpx.RequestID)
	// Client IP is resolved once, before anything that records or limits by
	// address, so every consumer sees the same answer.
	r.Use(deps.ClientIPs.Middleware)
	// Every audit write below inherits the request origin from the context.
	r.Use(requestMeta)
	r.Use(httpx.Recoverer(deps.Logger))
	r.Use(metrics.Middleware(deps.recorder()))
	r.Use(httpx.AccessLog(deps.Logger))
	r.Use(httpx.SecureHeaders)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "Method not allowed for this resource")
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Cookie-borne sessions mean every write needs the cross-origin guard.
		r.Use(httpx.CheckOrigin)

		r.Get("/version", versionHandler(deps.Version))

		for _, module := range deps.Modules {
			module.Mount(r)
		}
	})

	return r
}

// NewInternalRouter builds the operational router. It carries no access
// logging: scrape traffic would drown the request log.
func NewInternalRouter(deps Deps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(httpx.Recoverer(deps.Logger))

	// Only a backend with a pull endpoint gets /metrics. Serving an empty page
	// for the log or disabled backends would tell a scraper the service is
	// instrumented when its numbers live somewhere else entirely.
	if scraper, ok := deps.recorder().(metrics.Scraper); ok {
		r.Handle("/metrics", scraper.ScrapeHandler())
	}

	r.Handle("/healthz", health.Live())
	r.Handle("/readyz", health.Ready(health.Options{
		Logger:    deps.Logger,
		Timeout:   deps.readinessTimeout(),
		CacheMode: deps.CacheMode,
		Checkers:  deps.Checkers,
	}))

	return r
}

func versionHandler(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, map[string]string{"version": version})
	}
}
