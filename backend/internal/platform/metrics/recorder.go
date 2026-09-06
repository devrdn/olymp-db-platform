// Package metrics records request statistics through a pluggable backend.
//
// Prometheus is the default, but it is not a hard dependency: a deployment
// that only ships logs can report through the log backend, and one that wants
// no instrumentation at all can turn it off. The call site is identical in
// every case, so no handler ever needs to know which backend is active.
package metrics

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Backend names accepted by New.
const (
	BackendPrometheus = "prometheus"
	BackendLog        = "log"
	BackendNone       = "none"
)

// Recorder receives one observation per served request.
//
// Implementations must be safe for concurrent use and must never fail: metrics
// are diagnostics, and losing them may not affect serving a contest.
//
// streaming marks a long-lived response — an SSE channel that can stay open
// for the length of a contest (internal/api/events_handler.go) — whose own
// duration is not what the shared request-duration histogram's buckets, or
// the p99 read off them, are meant to describe (finding 5): one such
// connection would otherwise be the single slowest "request" the service
// ever serves, on every scrape, and would own that p99 outright. A streaming
// request is still counted; its duration lands in a histogram of its own
// instead of the one every ordinary request shares. Middleware sets it from
// whatever the handler told it via MarkStreaming — a call site that never
// does so always passes false, which is the correct default for every
// existing request in this service.
type Recorder interface {
	ObserveRequest(method, route string, status int, d time.Duration, streaming bool)
}

// Scraper is the optional interface for backends that expose a pull endpoint.
// The internal router registers /metrics only for recorders that implement it.
type Scraper interface {
	ScrapeHandler() http.Handler
}

// Runner is the optional interface for backends that need a background loop.
type Runner interface {
	// Run reports until ctx is cancelled.
	Run(stop <-chan struct{})
}

// New builds the recorder named by backend.
func New(backend string, log *slog.Logger) (Recorder, error) {
	switch backend {
	case BackendPrometheus, "":
		return NewPrometheus(), nil
	case BackendLog:
		return NewLog(log, defaultReportInterval), nil
	case BackendNone:
		return Noop{}, nil
	default:
		return nil, fmt.Errorf("unknown metrics backend %q, want one of %s, %s, %s",
			backend, BackendPrometheus, BackendLog, BackendNone)
	}
}

// Noop discards observations. It exists so "metrics disabled" is a backend
// like any other rather than a nil check at every call site.
type Noop struct{}

// ObserveRequest does nothing.
func (Noop) ObserveRequest(string, string, int, time.Duration, bool) {}
