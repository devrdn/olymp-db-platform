package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

// unknownRoute groups requests that matched no route; the raw path would let
// any client create unbounded time series.
const unknownRoute = "unknown"

// otherMethod groups methods this service does not serve. HTTP allows any
// token as a method, so labelling each would let an unauthenticated caller
// create unbounded series.
const otherMethod = "other"

var knownMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodOptions: {},
}

func requestMethod(method string) string {
	if _, known := knownMethods[method]; known {
		return method
	}
	return otherMethod
}

// Middleware instruments a handler chain with the given recorder. It must run
// inside the router, where the matched route pattern is known.
func Middleware(rec Recorder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			sr := httpx.NewStatusRecorder(w)

			// MarkStreaming sets this from inside the handler; it is read
			// after the handler returns.
			streaming := new(bool)
			r = r.WithContext(context.WithValue(r.Context(), streamingKey{}, streaming))

			next.ServeHTTP(sr, r)

			rec.ObserveRequest(requestMethod(r.Method), routePattern(r), sr.Status(), time.Since(started), *streaming)
		})
	}
}

// routePattern returns the chi route template ("/contests/{contestID}"), so
// path parameters collapse into one series.
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return unknownRoute
}
