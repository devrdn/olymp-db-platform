package metrics

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestTheProvisioningQueueIsReadAtScrapeTime(t *testing.T) {
	p := NewPrometheus()
	var depth atomic.Int64
	depth.Store(4)
	WatchProvisioningQueue(p, func() int { return int(depth.Load()) })

	for _, value := range []string{"4", "0"} {
		want := `
# HELP provisioning_queue_depth Database copies this process was asked for and has not started, per process; aggregate with sum().
# TYPE provisioning_queue_depth gauge
provisioning_queue_depth ` + value + `
`
		if err := testutil.GatherAndCompare(p.Registry(), strings.NewReader(want), "provisioning_queue_depth"); err != nil {
			t.Fatal(err)
		}
		depth.Store(0)
	}
}

func TestWatchProvisioningQueueDoesNothingOnNonPrometheusBackends(t *testing.T) {
	t.Run("log", func(t *testing.T) {
		var buf bytes.Buffer
		rec := NewLog(logging.New("info", &buf), time.Minute)
		WatchProvisioningQueue(rec, func() int { return 7 })
		rec.Flush()

		if strings.Contains(buf.String(), "provisioning_") {
			t.Errorf("the log digest mentions the provisioning queue: %q", buf.String())
		}
	})
	t.Run("none", func(t *testing.T) {
		WatchProvisioningQueue(Noop{}, func() int { return 7 })
	})
}
