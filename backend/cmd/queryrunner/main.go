// Command queryrunner runs the DB Contest Query Runner.
//
// The one component that is a separate service (architecture, section 2.3),
// and this is the process it becomes. Two reasons it is not part of the Core
// API, and the first is the one that matters:
//
//   - It links PostgreSQL's own parser through cgo, in order to check a query
//     before running it. That is C code reading text an adversary chooses, and
//     a crash in it ends the process rather than raising something a recover
//     can catch. Inside the Core API, a crafted query would be a repeatable
//     way to end sign-in, the timer and the submission of answers.
//   - Only this process holds the game cluster's credentials.
//
// It is stateless and holds no core database connection: what it knows about a
// request is what the request carries.
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
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	// Self-check mode for the container health check: the runtime image has no
	// shell and no grpc probe, so the binary dials its own listener.
	healthcheck := flag.Bool("healthcheck", false, "probe the service and exit")
	flag.Parse()

	if *healthcheck {
		// Only the listen address is read, so a missing database DSN cannot
		// make the health check fail for the wrong reason.
		if err := rpc.Probe(context.Background(), rpc.ProbeAddress(os.Getenv("QUERY_RUNNER_ADDR"))); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		// The logger may not exist yet when configuration fails, so startup
		// errors go to stderr directly.
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

	cluster, err := queryrunner.NewCluster(cfg.GameDBDSN)
	if err != nil {
		return err
	}

	limits := queryrunner.Limits{
		Deadline:   cfg.Deadline,
		MaxRows:    cfg.MaxRows,
		MaxBytes:   cfg.MaxBytes,
		Concurrent: cfg.Concurrent,
		QueueDepth: cfg.QueueDepth,
	}

	// A result budget larger than what the transport will carry produces the
	// worst kind of failure: a valid answer arriving as a transport error,
	// which reads as the service being down. Refusing at startup is the only
	// place the two numbers can be compared before a participant meets them.
	if limits.MaxBytes >= rpc.MaxPayloadBytes {
		return fmt.Errorf(
			"QUERY_MAX_BYTES is %d, which does not leave room inside the %d byte message limit",
			limits.MaxBytes, rpc.MaxPayloadBytes)
	}

	runner := queryrunner.New(cluster, sqlpolicy.NewChecker(cfg.ExtraFunctions...), limits)

	// Shut down on SIGINT/SIGTERM: the container runtime sends SIGTERM and
	// waits before killing the process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	lis, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}

	return rpc.Serve(ctx, lis, rpc.NewServer(runner, limits, log), cfg.ShutdownTimeout, log)
}
