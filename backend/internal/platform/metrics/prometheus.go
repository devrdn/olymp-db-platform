package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Prometheus records into a private registry and exposes a scrape endpoint.
//
// The registry is per-instance rather than the package-global default, so
// tests observe an isolated set of series and two services in one process
// cannot clash.
type Prometheus struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewPrometheus returns a recorder backed by a registry that already carries
// the Go runtime and process collectors.
func NewPrometheus() *Prometheus {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	p := &Prometheus{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(p.requests, p.duration)

	return p
}

// ObserveRequest records one request.
func (p *Prometheus) ObserveRequest(method, route string, status int, d time.Duration) {
	p.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	p.duration.WithLabelValues(method, route).Observe(d.Seconds())
}

// ScrapeHandler serves the exposition endpoint.
func (p *Prometheus) ScrapeHandler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// Registry lets other components register their own collectors (database pool
// statistics, provisioning queue depth).
func (p *Prometheus) Registry() *prometheus.Registry {
	return p.registry
}
