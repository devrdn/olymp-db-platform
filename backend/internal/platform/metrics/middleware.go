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

// otherMethod groups request methods that are not ones this service serves.
//
// HTTP allows any token as a method, so the method is a string the client
// invents just as the path is, and a label per method is a series per
// invention — each one retained until the process restarts, from an
// unauthenticated caller.
const otherMethod = "other"

// knownMethods are the methods the API serves, and the only ones that reach a
// label of their own.
var knownMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
}

// requestMethod returns the method to label a request with: its own, when the
// service serves that method, and otherMethod for anything else.
func requestMethod(method string) string {
	if _, known := knownMethods[method]; known {
		return method
	}
	return otherMethod
}

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

			rec.ObserveRequest(requestMethod(r.Method), routePattern(r), sr.Status(), time.Since(started), *streaming)
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
