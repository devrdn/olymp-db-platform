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
	// streamDuration is where a long-lived response's own duration lands
	// instead of duration (finding 5) — see Recorder.ObserveRequest's own
	// doc for why the two must not share buckets.
	streamDuration *prometheus.HistogramVec
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
		// Seconds through hours, not milliseconds through seconds: a
		// streaming response's "duration" is how long the connection stayed
		// open, which for the events channel can be the length of a whole
		// contest (finding 5).
		streamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_stream_duration_seconds",
			Help:    "Duration of long-lived streaming responses (e.g. SSE) by method and route.",
			Buckets: []float64{1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200, 14400},
		}, []string{"method", "route"}),
	}
	reg.MustRegister(p.requests, p.duration, p.streamDuration)

	return p
}

// ObserveRequest records one request. streaming routes its duration into
// http_stream_duration_seconds instead of http_request_duration_seconds —
// see Recorder.ObserveRequest's own doc (finding 5). The request count is
// unaffected either way.
func (p *Prometheus) ObserveRequest(method, route string, status int, d time.Duration, streaming bool) {
	p.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	if streaming {
		p.streamDuration.WithLabelValues(method, route).Observe(d.Seconds())
		return
	}
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
