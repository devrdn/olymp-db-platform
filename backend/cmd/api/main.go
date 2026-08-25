// Command api runs the DB Contest Core API.
//
// Assembly lives in internal/app; this file is flags, signals and exit codes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/devrdn/db-contest/backend/internal/app"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	// Self-check mode for the container health check: the runtime image has no
	// shell and no curl, so the binary probes its own internal listener.
	healthcheck := flag.Bool("healthcheck", false, "probe the internal health endpoint and exit")
	flag.Parse()

	if *healthcheck {
		// Only the listener address is read, so a missing database DSN cannot
		// make the health check fail for the wrong reason.
		if err := health.Probe(context.Background(), app.ProbeURL(os.Getenv("INTERNAL_ADDR"))); err != nil {
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
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// Shut down on SIGINT/SIGTERM: the container runtime sends SIGTERM and
	// waits before killing the process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	service, err := app.New(ctx, cfg, version)
	if err != nil {
		return err
	}

	return service.Run(ctx)
}
