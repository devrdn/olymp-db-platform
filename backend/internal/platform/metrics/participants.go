package metrics

import "github.com/prometheus/client_golang/prometheus"

// ParticipantGauge exports contest_active_participants.
type ParticipantGauge struct {
	active prometheus.Gauge
}

// NewParticipantGauge registers the series on a Prometheus recorder and
// returns a gauge that ignores every other backend. Call it once per registry.
func NewParticipantGauge(rec Recorder) *ParticipantGauge {
	p, ok := rec.(*Prometheus)
	if !ok {
		return &ParticipantGauge{}
	}
	g := &ParticipantGauge{active: prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "contest_active_participants",
		Help: "A count of the shared database as of the API's last count, the same on every replica; aggregate with max().",
	})}
	p.Registry().MustRegister(g.active)
	return g
}

// SetActive records the number of active participants.
func (g *ParticipantGauge) SetActive(n int) {
	if g.active != nil {
		g.active.Set(float64(n))
	}
}
