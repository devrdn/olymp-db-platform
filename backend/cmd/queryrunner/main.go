// Command queryrunner runs the DB Contest Query Runner. It is a separate
// service because it links PostgreSQL's parser through cgo, C code reading
// adversary text whose crash a recover cannot catch, and because only it
// holds the game cluster's credentials. It is stateless and has no core
// database connection.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
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

	limits := queryrunner.Limits{
		Deadline:    cfg.Deadline,
		MaxRows:     cfg.MaxRows,
		MaxBytes:    cfg.MaxBytes,
		Concurrent:  cfg.Concurrent,
		QueueDepth:  cfg.QueueDepth,
		PerMinute:   cfg.PerMinute,
		IdleTimeout: cfg.IdleConnTimeout,
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}

	return rpc.Serve(ctx, lis, rpc.NewServer(runner, limits, log), cfg.Token, cfg.ShutdownTimeout, log)
}
