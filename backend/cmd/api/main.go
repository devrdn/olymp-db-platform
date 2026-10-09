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

// version is set at build time with -ldflags.
var version = "dev"

func main() {
	// The runtime image has no shell or curl, so the binary probes itself.
	healthcheck := flag.Bool("healthcheck", false, "probe the internal health endpoint and exit")
	flag.Parse()

	if *healthcheck {
		// Only the listener address is read, so missing config cannot fail it.
		if err := health.Probe(context.Background(), app.ProbeURL(os.Getenv("INTERNAL_ADDR"))); err != nil {
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
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	service, err := app.New(ctx, cfg, version)
	if err != nil {
		return err
	}

	return service.Run(ctx)
}
