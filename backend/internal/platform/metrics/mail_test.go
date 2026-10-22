package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

var outcomes = []string{"sent", "retry", "abandoned"}

func TestMailCountersStartAtZeroForEveryKindAndOutcome(t *testing.T) {
	p := NewPrometheus()
	NewMailCounters(p, []string{"sign_in_details", "other"}, outcomes)

	want := `
# HELP mail_messages_total Outbox messages by how their delivery attempt ended: sent, retry (a temporary failure, tried again later) or abandoned (never sent).
# TYPE mail_messages_total counter
mail_messages_total{kind="other",outcome="abandoned"} 0
mail_messages_total{kind="other",outcome="retry"} 0
mail_messages_total{kind="other",outcome="sent"} 0
mail_messages_total{kind="sign_in_details",outcome="abandoned"} 0
mail_messages_total{kind="sign_in_details",outcome="retry"} 0
mail_messages_total{kind="sign_in_details",outcome="sent"} 0
`
	if err := testutil.GatherAndCompare(p.Registry(), strings.NewReader(want), "mail_messages_total"); err != nil {
		t.Fatal(err)
	}
}

func TestMailCountersCountOutcomesAndReportTheDueRows(t *testing.T) {
	p := NewPrometheus()
	mail := NewMailCounters(p, []string{"sign_in_details"}, outcomes)

	mail.Count("sign_in_details", "sent")
	mail.Count("sign_in_details", "sent")
	mail.Count("sign_in_details", "abandoned")
	mail.SetDue(7)

	if got := testutil.ToFloat64(mail.messages.WithLabelValues("sign_in_details", "sent")); got != 2 {
		t.Errorf("sent = %v, want 2", got)
	}
	if got := testutil.ToFloat64(mail.messages.WithLabelValues("sign_in_details", "abandoned")); got != 1 {
		t.Errorf("abandoned = %v, want 1", got)
	}
	if got := testutil.ToFloat64(mail.due); got != 7 {
		t.Errorf("due = %v, want 7", got)
	}
}

// A label value outside the declared sets would be a new series per value, so
// it is dropped rather than counted.
func TestMailCountersIgnoreAKindOrOutcomeTheyWereNotGiven(t *testing.T) {
	p := NewPrometheus()
	mail := NewMailCounters(p, []string{"sign_in_details"}, outcomes)

	mail.Count("invented", "sent")
	mail.Count("sign_in_details", "bounced")

	if n := testutil.CollectAndCount(mail.messages); n != 3 {
		t.Errorf("mail_messages_total has %d series, want the 3 declared ones", n)
	}
}

func TestMailCountersDoNothingOnNonPrometheusBackends(t *testing.T) {
	for name, rec := range allBackends(t) {
		if name == "prometheus" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			mail := NewMailCounters(rec, []string{"sign_in_details"}, outcomes)
			mail.Count("sign_in_details", "sent") // must not panic
			mail.SetDue(3)
		})
	}
}
