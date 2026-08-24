// Package health exposes the liveness and readiness endpoints used by the
// container runtime and the reverse proxy.
//
// The two probes answer different questions. Liveness asks "is this process
// still working?" — restarting helps only if the answer is no. Readiness asks
// "can this instance serve traffic right now?", which depends on the database
// and cache being reachable.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// Status values reported per dependency.
const (
	statusOK     = "ok"
	statusFailed = "failed"
)

// Checker probes one dependency of the service.
type Checker interface {
	// Name identifies the dependency in the readiness response.
	Name() string
	// Check returns nil when the dependency is usable.
	Check(ctx context.Context) error
}

// CheckerFunc adapts a function to the Checker interface.
type CheckerFunc struct {
	CheckerName string
	Probe       func(ctx context.Context) error
}

func (c CheckerFunc) Name() string { return c.CheckerName }

func (c CheckerFunc) Check(ctx context.Context) error { return c.Probe(ctx) }

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
	// Cache names the active cache backend. A service running on the
	// in-process fallback behaves normally per instance, so without this the
	// degradation is invisible from the outside.
	Cache string `json:"cache,omitempty"`
}

// Options configures the readiness handler.
type Options struct {
	Logger    *slog.Logger
	Timeout   time.Duration
	CacheMode string
	Checkers  []Checker
}

func (o Options) logger() *slog.Logger {
	if o.Logger == nil {
		return slog.Default()
	}
	return o.Logger
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return defaultReadinessTimeout
}

// defaultReadinessTimeout bounds the probes when the caller sets none.
const defaultReadinessTimeout = 3 * time.Second

// Live reports that the process is running. It deliberately performs no
// dependency checks, so a database outage does not trigger a restart loop.
func Live() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, response{Status: statusOK})
	})
}

// Ready probes every dependency and reports 503 if any of them is unusable.
// Probes run concurrently under a shared timeout, so one stalled dependency
// cannot hang the endpoint.
//
// Failure details are logged, never returned: this endpoint is reachable from
// the network and driver errors carry hosts, ports and user names.
func Ready(opts Options) http.Handler {
	log := opts.logger()
	timeout := opts.timeout()
	checkers := opts.Checkers

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks := make(map[string]string, len(checkers))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, c := range checkers {
			wg.Add(1)
			go func(c Checker) {
				defer wg.Done()

				status := statusOK
				if err := c.Check(ctx); err != nil {
					status = statusFailed
					log.WarnContext(ctx, "readiness check failed",
						"dependency", c.Name(),
						"error", err,
					)
				}

				mu.Lock()
				checks[c.Name()] = status
				mu.Unlock()
			}(c)
		}
		wg.Wait()

		body := response{Status: statusOK, Checks: checks, Cache: opts.CacheMode}
		code := http.StatusOK
		for _, status := range checks {
			if status == statusFailed {
				body.Status = "unavailable"
				code = http.StatusServiceUnavailable
				break
			}
		}

		httpx.JSON(w, r, code, body)
	})
}
