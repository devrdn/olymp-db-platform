package metrics

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// unknownRoute groups requests that matched no route. Using the raw path here
// would let any client create unbounded time series by sending random URLs.
const unknownRoute = "unknown"

// Middleware instruments a handler chain with the given recorder. The
// instrumentation is identical for every backend, so switching backends cannot
// change what is measured — only where it goes.
//
// It must run inside the router, where the matched route pattern is known.
func Middleware(rec Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(sr, r)

			rec.ObserveRequest(r.Method, routePattern(r), sr.status, time.Since(started))
		})
	}
}

// routePattern returns the chi route template ("/contests/{contestID}") so
// path parameters collapse into a single series.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return unknownRoute
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap exposes the wrapped writer to http.ResponseController, keeping
// streaming responses (SSE) usable behind this middleware.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
