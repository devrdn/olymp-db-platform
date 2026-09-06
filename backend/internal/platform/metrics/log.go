package metrics

import (
	"log/slog"
	"sync"
	"time"
)

// defaultReportInterval is how often the log backend summarises traffic. It is
// coarse on purpose: the point is a periodic digest, not a per-request echo of
// what the access log already records.
const defaultReportInterval = time.Minute

// Log aggregates request statistics in memory and writes a periodic digest to
// the logger.
//
// It is the fallback for deployments that collect logs but run no Prometheus:
// counts and latency still reach the same place as everything else, at the
// cost of resolution and of any query language over the numbers.
type Log struct {
	log      *slog.Logger
	interval time.Duration

	mu      sync.Mutex
	buckets map[bucketKey]*bucket
}

type bucketKey struct {
	method string
	route  string
	status int
	// streaming keeps a long-lived response's aggregate apart from ordinary
	// requests on the same route (finding 5) — the same reasoning
	// Prometheus.streamDuration exists for, applied to this backend's own
	// digest instead of a histogram.
	streaming bool
}

type bucket struct {
	count   int64
	totalNs int64
	maxNs   int64
}

// NewLog returns a recorder that reports through log records.
func NewLog(log *slog.Logger, interval time.Duration) *Log {
	if interval <= 0 {
		interval = defaultReportInterval
	}
	return &Log{
		log:      log,
		interval: interval,
		buckets:  map[bucketKey]*bucket{},
	}
}

// ObserveRequest folds one request into its aggregate. streaming keeps a
// long-lived response's own aggregate apart from ordinary requests on the
// same route (finding 5) — Flush reports the two separately rather than
// blending a connection that can last a whole contest into the same average
// and max as everything else on that route.
func (l *Log) ObserveRequest(method, route string, status int, d time.Duration, streaming bool) {
	key := bucketKey{method: method, route: route, status: status, streaming: streaming}

	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		b = &bucket{}
		l.buckets[key] = b
	}

	ns := d.Nanoseconds()
	b.count++
	b.totalNs += ns
	if ns > b.maxNs {
		b.maxNs = ns
	}
}

// Run writes a digest every interval until stop is closed, then writes a final
// one so the last window is not lost on shutdown.
func (l *Log) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.Flush()
		case <-stop:
			l.Flush()
			return
		}
	}
}

// Flush writes the current window and resets the counters. Counters reset so
// each record describes one interval rather than all history.
func (l *Log) Flush() {
	l.mu.Lock()
	buckets := l.buckets
	l.buckets = map[bucketKey]*bucket{}
	l.mu.Unlock()

	for key, b := range buckets {
		if b.count == 0 {
			continue
		}
		l.log.Info("http metrics",
			"method", key.method,
			"route", key.route,
			"status", key.status,
			"streaming", key.streaming,
			"count", b.count,
			"avg_ms", (b.totalNs/b.count)/int64(time.Millisecond),
			"max_ms", b.maxNs/int64(time.Millisecond),
		)
	}
}
