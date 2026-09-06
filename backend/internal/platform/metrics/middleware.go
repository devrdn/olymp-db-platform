package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
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
			sr := httpx.NewStatusRecorder(w)

			// A fresh mailbox per request: MarkStreaming flips it from inside
			// the handler, once it knows this response will be a long-lived
			// stream rather than an ordinary one (finding 5) — read back here,
			// after the handler has returned, since that is the only point
			// this middleware runs any code of its own again.
			streaming := new(bool)
			r = r.WithContext(context.WithValue(r.Context(), streamingKey{}, streaming))

			next.ServeHTTP(sr, r)

			rec.ObserveRequest(r.Method, routePattern(r), sr.Status(), time.Since(started), *streaming)
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
