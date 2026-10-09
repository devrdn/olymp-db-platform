// Package api is the HTTP surface of the Core API: it adapts requests to the
// domain packages and answers their errors with declared codes. Domain rules
// live in the domain packages and HTTP plumbing in platform/httpx, not here.
//
// Two routers are built from the same dependencies. The public router is the
// only one published through the reverse proxy; the internal router serves
// metrics, liveness and readiness on a port that is never exposed.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/go-chi/chi/v5"
)

// defaultReadinessTimeout bounds the dependency probes so the endpoint always
// answers, even when a dependency hangs instead of refusing.
const defaultReadinessTimeout = 3 * time.Second

// Deps carries everything the routers need.
type Deps struct {
	Logger *slog.Logger
	// Metrics is any recorder; the router adapts to what the backend offers.
	Metrics  metrics.Recorder
	Version  string
	Checkers []health.Checker
	// ClientIPs resolves the real client address behind the reverse proxy. The
	// zero value trusts nobody: forwarded headers are ignored and the TCP peer
	// is the client.
	ClientIPs httpx.IPResolver
	// PublicOrigins are the front origins CheckOrigin accepts on top of this
	// service's own host. Empty means same-origin only.
	PublicOrigins []string
	// CacheMode names the active cache backend, reported by readiness so a
	// degraded install is visible to operators.
	CacheMode        string
	ReadinessTimeout time.Duration
	// Modules contribute the routes of a feature area under /api/v1; a feature
	// is added by wiring one in, not by editing this package.
	Modules []Module
}

// Module registers the routes of one feature area.
type Module interface {
	Mount(r chi.Router)
}

// chiRouter is the router type a module receives.
type chiRouter = chi.Router

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

	// Order matters: request identity first so every later record carries it,
	// then the client address, then observability and hardening, with recovery
	// innermost.
	r.Use(httpx.RequestID)
	// Resolved once, before anything that records or limits by address.
	r.Use(deps.ClientIPs.Middleware)
	// Every audit write below inherits the request origin from the context.
	r.Use(requestMeta)
	// Observability outside recovery: a panic unwinds past anything that
	// records after calling next, so with the recoverer outermost a panicking
	// request would leave no access log line, metric or 500 in the counts.
	r.Use(httpx.AccessLog(deps.Logger))
	r.Use(metrics.Middleware(deps.recorder()))
	r.Use(httpx.SecureHeaders)
	r.Use(httpx.Recoverer(deps.Logger))
	r.Use(refuseUnstorableQuery)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, http.StatusMethodNotAllowed, codeMethodNotAllowed, "Method not allowed for this resource")
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Cookie-borne sessions mean every write needs the cross-origin guard.
		r.Use(httpx.CheckOrigin(deps.PublicOrigins))

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

	// Only a backend with a pull endpoint gets /metrics; an empty page would
	// falsely suggest the numbers are here.
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

// refuseUnstorableQuery answers 400 for a decoded path or query string holding
// a NUL byte or invalid UTF-8, on every route. No stored text can contain
// either, and PostgreSQL would fail the statement, turning the client's
// malformed address into a 500. httpx.DecodeJSON applies the same rule to JSON
// bodies; multipart fields and stored headers are checked where read.
func refuseUnstorableQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !utf8.ValidString(r.URL.Path) || strings.ContainsRune(r.URL.Path, 0) {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
				"The path holds a NUL character or bytes that are not UTF-8")
			return
		}
		for key, values := range r.URL.Query() {
			for _, text := range append(values, key) {
				if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
					httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
						"A query parameter holds a NUL character or bytes that are not UTF-8")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
