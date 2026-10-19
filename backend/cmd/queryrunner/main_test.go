package main

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
	"github.com/google/uuid"
)

func TestTheDefaultIdleBudgetAlwaysFitsTheRoleConnectionLimit(t *testing.T) {
	if idle, err := idleConnections(8, -1); err != nil || idle != 32 {
		t.Fatalf("at the default concurrency: %d, %v; want four per running query", idle, err)
	}
	for concurrent := 1; concurrent <= gamedb.RoleConnectionLimit; concurrent++ {
		if _, err := idleConnections(concurrent, -1); err != nil {
			t.Fatalf("QUERY_CONCURRENT=%d with the default idle budget was refused: %v", concurrent, err)
		}
	}
}

func TestAnIdleBudgetPastTheRoleConnectionLimitIsRefused(t *testing.T) {
	if _, err := idleConnections(8, gamedb.RoleConnectionLimit-8); err != nil {
		t.Fatalf("a budget exactly at the limit was refused: %v", err)
	}
	if _, err := idleConnections(8, gamedb.RoleConnectionLimit-7); err == nil {
		t.Fatal("a budget one past the limit was accepted")
	}
}

// The listener serves the runner's series as the command wires them: a
// refusal made by the runner shows on a scrape of the real socket.
func TestTheMetricsListenerServesTheRunnersSeries(t *testing.T) {
	// Nothing listens there; a refusal is decided before any connection.
	cluster, err := queryrunner.NewCluster("postgres://nobody:nothing@127.0.0.1:1/postgres", "")
	if err != nil {
		t.Fatalf("building the cluster connector: %v", err)
	}
	runner := queryrunner.New(cluster, checker.NewChecker(), queryrunner.DefaultLimits())
	t.Cleanup(runner.Close)

	prom := metrics.NewPrometheus()
	observe(prom, runner)

	if _, err := runner.Run(t.Context(), queryrunner.Request{
		Registration: uuid.New(), Database: "nowhere", SQL: "SELEC 1", Policy: sqlpolicy.ReadOnly(),
	}); err == nil {
		t.Fatal("a query that does not parse was run")
	}

	listener := metricsServer("127.0.0.1:0", prom, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := listener.Start(); err != nil {
		t.Fatalf("starting the metrics listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Shutdown(t.Context()) })

	body := get(t, "http://"+listener.Addr()+"/metrics", http.StatusOK)
	for _, want := range []string{
		`queryrunner_queries_total{outcome="rejected",reason="parse_error"} 1`,
		`queryrunner_queries_total{outcome="timeout",reason="queue"} 0`,
		`queryrunner_result_cutoffs_total{limit="rendered"} 0`,
		"queryrunner_active_queries 0",
		"queryrunner_admission_queue_depth 0",
		"queryrunner_admission_wait_seconds_count 0",
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the scrape is missing %s", want)
		}
	}

	// Nothing but /metrics is served there.
	get(t, "http://"+listener.Addr()+"/", http.StatusNotFound)
}

func get(t *testing.T, url string, status int) string {
	t.Helper()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", url, err)
	}
	if response.StatusCode != status {
		t.Fatalf("GET %s: status = %d, want %d", url, response.StatusCode, status)
	}
	return string(body)
}
