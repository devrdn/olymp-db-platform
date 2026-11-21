package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestParticipantGaugeStartsAtZeroAndHoldsTheLastCount(t *testing.T) {
	p := NewPrometheus()
	gauge := NewParticipantGauge(p)

	for _, value := range []string{"0", "42"} {
		want := `
# HELP contest_active_participants A count of the shared database as of the API's last count, the same on every replica; aggregate with max().
# TYPE contest_active_participants gauge
contest_active_participants ` + value + `
`
		if err := testutil.GatherAndCompare(p.Registry(), strings.NewReader(want), "contest_active_participants"); err != nil {
			t.Fatal(err)
		}
		gauge.SetActive(42)
	}
}

func TestParticipantGaugeDoesNothingOnNonPrometheusBackends(t *testing.T) {
	t.Run("log", func(t *testing.T) {
		var buf bytes.Buffer
		rec := NewLog(logging.New("info", &buf), time.Minute)
		NewParticipantGauge(rec).SetActive(7)
		rec.Flush()

		if strings.Contains(buf.String(), "contest_active_participants") {
			t.Errorf("the log digest mentions the gauge: %q", buf.String())
		}
	})
	t.Run("none", func(t *testing.T) {
		NewParticipantGauge(Noop{}).SetActive(7) // must not panic
	})
	t.Run("prometheus exports one series", func(t *testing.T) {
		p := NewPrometheus()
		NewParticipantGauge(p).SetActive(7)
		if got := testutil.CollectAndCount(p.Registry(), "contest_active_participants"); got != 1 {
			t.Errorf("series = %d, want 1", got)
		}
	})
}
