// Package metrics records request statistics through a pluggable backend:
// Prometheus by default, a periodic log digest, or nothing. The call site is
// the same for every backend, so no handler knows which one is active.
//
// It does not mount the scrape endpoint; internal/api does, on the internal
// router.
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

// Recorder receives one observation per served request. Implementations must
// be safe for concurrent use and must never fail.
//
// streaming marks a long-lived response (an SSE channel open for a whole
// contest). It is still counted, but its duration goes to a histogram of its
// own, or that one connection would own the request p99. Middleware sets it
// from MarkStreaming; it is false otherwise.
type Recorder interface {
	ObserveRequest(method, route string, status int, d time.Duration, streaming bool)
}

// Scraper is the optional interface for backends that expose a pull endpoint;
// /metrics is registered only for recorders that implement it.
type Scraper interface {
	ScrapeHandler() http.Handler
}

// Runner is the optional interface for backends that need a background loop.
type Runner interface {
	// Run reports until stop is closed.
	Run(stop <-chan struct{})
}

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

// Noop discards observations, so "metrics disabled" needs no nil checks.
type Noop struct{}

func (Noop) ObserveRequest(string, string, int, time.Duration, bool) {}
