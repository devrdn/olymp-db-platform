package metrics

import "github.com/prometheus/client_golang/prometheus"

// WatchProvisioningQueue exports provisioning_queue_depth, read from depth at
// scrape time. depth must be safe for concurrent use; call it once per registry.
func WatchProvisioningQueue(rec Recorder, depth func() int) {
	p, ok := rec.(*Prometheus)
	if !ok {
		return
	}
	p.Registry().MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "provisioning_queue_depth",
		Help: "Database copies this process was asked for and has not started, per process; aggregate with sum().",
	}, func() float64 { return float64(depth()) }))
}
