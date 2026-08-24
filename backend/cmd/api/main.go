// Command api runs the DB Contest Core API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// version is stamped at build time with -ldflags.
var version = "dev"

func main() {
	// Self-check mode for the container health check: the runtime image has no
	// shell and no curl, so the binary probes its own internal listener.
	healthcheck := flag.Bool("healthcheck", false, "probe the internal health endpoint and exit")
	flag.Parse()

	if *healthcheck {
		if err := runHealthcheck(); err != nil {
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

// runHealthcheck probes the liveness endpoint on the internal listener. It
// reads only the listener address, so a missing database DSN does not make the
// health check fail for the wrong reason.
func runHealthcheck() error {
	addr := os.Getenv("INTERNAL_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}

	return health.Probe(context.Background(), "http://"+addr+"/healthz")
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	log := logging.New(cfg.LogLevel, os.Stdout)
	log.Info("starting core api", "version", version, "env", cfg.Env)

	// Shut down on SIGINT/SIGTERM: the container runtime sends SIGTERM and
	// waits before killing the process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The core database is the one hard dependency: without it there are no
	// users, contests or answers to serve.
	pool, err := storage.NewPool(ctx, cfg.CoreDBDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	// The cache is optional. With no address configured the service runs on
	// the in-process store and says so loudly (see the cache package).
	cacheBackend, err := cache.New(ctx, cfg.RedisAddr, log)
	if err != nil {
		return err
	}
	defer cacheBackend.Close()

	// Metrics are diagnostics: the backend is pluggable and may be off
	// entirely, and nothing about serving changes either way.
	recorder, err := metrics.New(cfg.MetricsBackend, log)
	if err != nil {
		return fmt.Errorf("configure metrics: %w", err)
	}
	log.Info("metrics backend ready", "backend", cfg.MetricsBackend)

	// A backend that reports on a schedule needs a loop; one that is scraped
	// or disabled does not.
	if runner, ok := recorder.(metrics.Runner); ok {
		stop := make(chan struct{})
		defer close(stop)
		go runner.Run(stop)
	}

	deps := api.Deps{
		Logger:    log,
		Metrics:   recorder,
		Version:   version,
		CacheMode: cache.Mode(cacheBackend),
		Checkers: []health.Checker{
			storage.NewChecker("core-db", pool),
			storage.NewChecker("cache", cacheBackend),
		},
	}

	public := server.New("public", cfg.HTTPAddr, api.NewRouter(deps), log)
	internal := server.New("internal", cfg.InternalAddr, api.NewInternalRouter(deps), log)

	if err := public.Start(); err != nil {
		return fmt.Errorf("start public server: %w", err)
	}
	if err := internal.Start(); err != nil {
		// Stop the listener that already came up, so a failed startup leaves
		// no half-running process behind.
		_ = public.Shutdown(context.Background())
		return fmt.Errorf("start internal server: %w", err)
	}

	<-ctx.Done()
	log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	// Stop accepting public traffic first, then operational endpoints, so the
	// readiness probe keeps answering while requests drain.
	publicErr := public.Shutdown(shutdownCtx)
	internalErr := internal.Shutdown(shutdownCtx)

	if publicErr != nil {
		return fmt.Errorf("shut down public server: %w", publicErr)
	}
	if internalErr != nil {
		return fmt.Errorf("shut down internal server: %w", internalErr)
	}

	log.Info("core api stopped")
	return nil
}
