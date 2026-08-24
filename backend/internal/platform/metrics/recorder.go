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
type Recorder interface {
	ObserveRequest(method, route string, status int, d time.Duration)
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
func (Noop) ObserveRequest(string, string, int, time.Duration) {}
