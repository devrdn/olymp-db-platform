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
// The registry is per instance, not the global default, so tests and two
// services in one process cannot clash.
type Prometheus struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	// streamDuration holds streaming responses' durations (see Recorder).
	streamDuration *prometheus.HistogramVec
}

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
		// Seconds through hours: a stream can stay open for a whole contest.
		streamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_stream_duration_seconds",
			Help:    "Duration of long-lived streaming responses (e.g. SSE) by method and route.",
			Buckets: []float64{1, 5, 15, 30, 60, 300, 900, 1800, 3600, 7200, 14400},
		}, []string{"method", "route"}),
	}
	reg.MustRegister(p.requests, p.duration, p.streamDuration)

	return p
}

// ObserveRequest records one request. A streaming response's duration goes to
// http_stream_duration_seconds; the request count is the same either way.
func (p *Prometheus) ObserveRequest(method, route string, status int, d time.Duration, streaming bool) {
	p.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	if streaming {
		p.streamDuration.WithLabelValues(method, route).Observe(d.Seconds())
		return
	}
	p.duration.WithLabelValues(method, route).Observe(d.Seconds())
}

func (p *Prometheus) ScrapeHandler() http.Handler {
	return promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{})
}

// Registry lets other components register their own collectors.
func (p *Prometheus) Registry() *prometheus.Registry {
	return p.registry
}
