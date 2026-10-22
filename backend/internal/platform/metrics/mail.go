package metrics

import "github.com/prometheus/client_golang/prometheus"

// MailCounters are what the mail outbox reports: how each delivery attempt
// ended, by the message's kind, how many messages are due and waiting, and
// whether the relay itself is failing.
type MailCounters struct {
	messages      *prometheus.CounterVec
	due           prometheus.Gauge
	relayFailures prometheus.Counter
	relayFailing  prometheus.Gauge
	// declared holds kind + "\x00" + outcome for every exported series.
	declared map[string]bool
}

// NewMailCounters registers the set on rec's registry when rec is the
// Prometheus backend, and returns a set that discards otherwise. kinds and
// outcomes are the label values the caller declares; every pair is exported
// at zero from the start, so increase() sees the first abandoned message.
func NewMailCounters(rec Recorder, kinds, outcomes []string) *MailCounters {
	p, ok := rec.(*Prometheus)
	if !ok {
		return &MailCounters{}
	}

	c := &MailCounters{
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mail_messages_total",
			Help: "Outbox messages by how their delivery attempt ended: sent, retry (a temporary failure, tried again later) or abandoned (never sent).",
		}, []string{"kind", "outcome"}),
		due: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_outbox_due",
			Help: "Outbox messages due for sending and not yet claimed, as of the sender's last run. A count of the shared table, the same on every replica: aggregate with max(), not sum().",
		}),
		relayFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mail_relay_failures_total",
			Help: "Delivery attempts that failed at the relay rather than for the message; none is charged to the message.",
		}),
		relayFailing: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mail_relay_failing",
			Help: "1 while the relay's last answer was a failure of the relay itself (unreachable, TLS, a refused account), which pauses sending; 0 once it answers for a message.",
		}),
		declared: map[string]bool{},
	}
	for _, kind := range kinds {
		for _, outcome := range outcomes {
			c.messages.WithLabelValues(kind, outcome)
			c.declared[kind+"\x00"+outcome] = true
		}
	}
	p.Registry().MustRegister(c.messages, c.due, c.relayFailures, c.relayFailing)
	return c
}

// Count records one attempt's outcome. An undeclared pair is dropped, so a
// caller's mistake cannot grow the series without bound.
func (c *MailCounters) Count(kind, outcome string) {
	if c.messages == nil || !c.declared[kind+"\x00"+outcome] {
		return
	}
	c.messages.WithLabelValues(kind, outcome).Inc()
}

// SetDue records how many messages are due.
func (c *MailCounters) SetDue(n int) {
	if c.due != nil {
		c.due.Set(float64(n))
	}
}

// RelayFailed counts one attempt that failed at the relay.
func (c *MailCounters) RelayFailed() {
	if c.relayFailures != nil {
		c.relayFailures.Inc()
	}
}

// SetRelayFailing records whether the relay's last answer was its own failure.
func (c *MailCounters) SetRelayFailing(failing bool) {
	if c.relayFailing == nil {
		return
	}
	if failing {
		c.relayFailing.Set(1)
	} else {
		c.relayFailing.Set(0)
	}
}
