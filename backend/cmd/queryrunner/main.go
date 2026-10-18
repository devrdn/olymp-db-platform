// Command queryrunner runs the DB Contest Query Runner. It is a separate
// service because it links PostgreSQL's parser through cgo, C code reading
// adversary text whose crash a recover cannot catch, and because only it
// holds the participants' roles (game_reader, game_writer) and runs their
// SQL; the Core API reaches the game cluster only as the provisioning role.
// It is stateless and has no core database connection. Its metrics are served
// over plain HTTP on a second, internal listener (QUERY_RUNNER_METRICS_ADDR).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	// The runtime image has no shell or grpc probe, so the binary dials itself.
	healthcheck := flag.Bool("healthcheck", false, "probe the service and exit")
	flag.Parse()

	if *healthcheck {
		// Only the address and the token are read; the health service needs
		// the token like everything else.
		address := rpc.ProbeAddress(os.Getenv("QUERY_RUNNER_ADDR"))
		if err := rpc.Probe(context.Background(), address, os.Getenv("QUERY_RUNNER_TOKEN")); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		// No logger exists yet.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadRunner()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	log := logging.New(cfg.LogLevel, os.Stdout).With("service", "queryrunner", "version", version)

	cluster, err := queryrunner.NewCluster(cfg.GameDBDSN, cfg.GameDBWriterDSN)
	if err != nil {
		return err
	}

	idleConns, err := idleConnections(cfg.Concurrent, cfg.IdleConns)
	if err != nil {
		return err
	}

	limits := queryrunner.Limits{
		Deadline:    cfg.Deadline,
		MaxRows:     cfg.MaxRows,
		MaxBytes:    cfg.MaxBytes,
		Concurrent:  cfg.Concurrent,
		QueueDepth:  cfg.QueueDepth,
		QueueWait:   cfg.QueueWait,
		PerMinute:   cfg.PerMinute,
		IdleTimeout: cfg.IdleConnTimeout,
		IdleConns:   idleConns,
	}

	// A result budget past what the transport carries would turn a valid
	// answer into a transport error, so it is refused at startup.
	if limits.MaxBytes >= rpc.MaxPayloadBytes {
		return fmt.Errorf(
			"QUERY_MAX_BYTES is %d, which does not leave room inside the %d byte message limit",
			limits.MaxBytes, rpc.MaxPayloadBytes)
	}

	runner := queryrunner.New(cluster, checker.NewChecker(cfg.ExtraFunctions...), limits)
	// After the server stops, close the kept connections.
	defer runner.Close()

	prom := metrics.NewPrometheus()
	observed := observe(prom, runner)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metricsListener := metricsServer(cfg.MetricsAddr, prom, log)
	if err := metricsListener.Start(); err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.MetricsAddr, err)
	}
	defer func() {
		ending, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
		defer cancel()
		if err := metricsListener.Shutdown(ending); err != nil {
			log.Warn("metrics listener did not stop cleanly", "error", err)
		}
	}()

	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}

	return rpc.Serve(ctx, lis, rpc.NewServer(runner, limits, log).WithCutOffs(observed), cfg.Token, cfg.ShutdownTimeout, log)
}

// metricsShutdownTimeout bounds waiting for a scrape in flight; a scrape is
// milliseconds of work.
const metricsShutdownTimeout = 5 * time.Second

// observe registers the runner's series on prom and attaches them to the
// runner. The result also takes the gRPC server's rendered cut-offs.
func observe(prom *metrics.Prometheus, runner *queryrunner.Runner) *metrics.QueryRunner {
	observed := metrics.NewQueryRunner(prom, queryrunner.Endings(), queryrunner.CutOffs())
	runner.WithObserver(observed)
	observed.WatchAdmission(runner.Admission)
	return observed
}

// metricsServer serves /metrics on a listener of its own: the gRPC port
// demands the token on every call, which a scraper does not carry, and
// scraping must not share a port with participants' queries.
func metricsServer(addr string, prom *metrics.Prometheus, log *slog.Logger) *server.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", prom.ScrapeHandler())
	return server.New("metrics", addr, mux, log)
}

// idleConnections settles how many idle connections the runner keeps:
// configured, or by default four per running query. Running and idle ones
// connect as the participant roles, so past their CONNECTION LIMIT a query
// fails to connect instead of being told the system is busy. The default
// leaves as many connections free as run, for backends still exiting.
func idleConnections(concurrent, configured int) (int, error) {
	idle := configured
	if idle < 0 {
		idle = max(0, min(4*concurrent, gamedb.RoleConnectionLimit-2*concurrent))
	}
	if concurrent+idle > gamedb.RoleConnectionLimit {
		return 0, fmt.Errorf(
			"QUERY_CONCURRENT=%d plus QUERY_IDLE_CONNS=%d is %d game-cluster connections, "+
				"past the participant roles' CONNECTION LIMIT of %d: lower QUERY_IDLE_CONNS or QUERY_CONCURRENT",
			concurrent, idle, concurrent+idle, gamedb.RoleConnectionLimit)
	}
	return idle, nil
}
