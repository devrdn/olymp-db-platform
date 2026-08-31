// Package app assembles the Core API from its parts and runs it.
//
// main used to do all of this inline, which had two costs: the wiring was
// untestable because it lived in func main, and every new module grew a
// 90-line run() further. The composition now lives here, where it has a name,
// an order, and a place for the next module to plug in; main is reduced to
// flags and exit codes.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// App is the assembled service: two HTTP listeners over one dependency graph.
type App struct {
	cfg      config.Config
	log      *slog.Logger
	public   *server.Server
	internal *server.Server

	// closers releases resources in reverse assembly order on shutdown.
	closers []func()
}

// New builds the service. Construction is fail-fast: an unreachable database,
// a bad metrics backend or an unparseable proxy list is reported here, before
// a single request is accepted.
func New(ctx context.Context, cfg config.Config, version string) (*App, error) {
	log := logging.New(cfg.LogLevel, os.Stdout)
	log.Info("starting core api", "version", version, "env", cfg.Env)

	a := &App{cfg: cfg, log: log}
	ok := false
	// Half-built resources must not leak when a later step fails.
	defer func() {
		if !ok {
			a.close()
		}
	}()

	// The core database is the one hard dependency: without it there are no
	// users, contests or answers to serve.
	pool, err := storage.NewPool(ctx, cfg.CoreDBDSN)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, pool.Close)

	// The cache is optional: with no address configured the service runs on
	// the in-process store and says so loudly (see the cache package).
	cacheBackend, err := cache.New(ctx, cfg.RedisAddr, log)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, func() { _ = cacheBackend.Close() })

	// Metrics are diagnostics: the backend is pluggable and may be off
	// entirely, and nothing about serving changes either way.
	recorder, err := metrics.New(cfg.MetricsBackend, log)
	if err != nil {
		return nil, fmt.Errorf("configure metrics: %w", err)
	}
	log.Info("metrics backend ready", "backend", cfg.MetricsBackend)

	// A backend that reports on a schedule needs a loop; one that is scraped
	// or disabled does not.
	if runner, ok := recorder.(metrics.Runner); ok {
		stop := make(chan struct{})
		a.closers = append(a.closers, func() { close(stop) })
		go runner.Run(stop)
	}

	// Forwarded headers are believed only from the configured proxies;
	// everything address-based (throttling, audit) sees the resolution.
	resolver, err := httpx.NewIPResolver(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("configure trusted proxies: %w", err)
	}

	// Authentication and account management. The repositories are the only
	// components that know SQL; everything above them works against the
	// interfaces the domain packages declare.
	userRepo := postgres.NewUsers(pool)
	auditRecorder := audit.New(postgres.NewAuditSink(pool))
	sessions := auth.NewSessionStore(cacheBackend, cfg.SessionTTL)
	cookies := auth.NewCookieWriter(cfg.CookieSecure)

	authService := auth.NewService(auth.ServiceConfig{
		Users:                 userRepo,
		Sessions:              sessions,
		Audit:                 auditRecorder,
		Limiter:               auth.NewLimiter(cacheBackend),
		Logger:                log,
		MaxAttemptsPerAddress: cfg.MaxLoginAttemptsPerAddress,
	})
	authMiddleware := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions:   sessions,
		Users:      userRepo,
		Authorizer: rbac.New(postgres.NewContestRoles(pool)),
		Cookies:    cookies,
		Logger:     log,
	})
	userService := users.NewService(userRepo, auditRecorder, storage.NewUnitOfWork(pool))

	// The contest module: everything an organizer authors and runs. It is
	// assembled from the same repositories pattern — the domain declares what
	// it needs, internal/postgres implements it — so nothing below this line
	// knows any SQL.
	contestService := contests.NewService(contests.ServiceConfig{
		Contests:      postgres.NewContests(pool),
		Stories:       postgres.NewStories(pool),
		Questions:     postgres.NewQuestions(pool),
		Managers:      postgres.NewContestManagers(pool),
		Registrations: postgres.NewRegistrations(pool),
		Policies:      postgres.NewSQLPolicies(pool),
		Languages:     postgres.NewLanguages(pool),
		Users:         userRepo,
		Audit:         auditRecorder,
		UnitOfWork:    storage.NewUnitOfWork(pool),
	})

	deps := api.Deps{
		Logger:    log,
		Metrics:   recorder,
		Version:   version,
		ClientIPs: resolver,
		CacheMode: cache.Mode(cacheBackend),
		Checkers: []health.Checker{
			storage.NewChecker("core-db", pool),
			storage.NewChecker("cache", cacheBackend),
		},
		Modules: []api.Module{
			api.NewAuthHandler(authService, userService, authMiddleware, cookies, log),
			api.NewUsersHandler(userService, authMiddleware, log),
			api.NewContestsHandler(contestService, authMiddleware, log, cfg.DefaultLocale),
			// The trail is written by every module above; this is the only way
			// to read it back, and it is behind its own permission.
			api.NewAuditHandler(postgres.NewAuditTrail(pool), authMiddleware, log),
		},
	}

	a.public = server.New("public", cfg.HTTPAddr, api.NewRouter(deps), log)
	a.internal = server.New("internal", cfg.InternalAddr, api.NewInternalRouter(deps), log)

	ok = true
	return a, nil
}

// Run serves until ctx is cancelled, then drains within the configured
// shutdown timeout. It always releases the app's resources before returning.
func (a *App) Run(ctx context.Context) error {
	defer a.close()

	if err := a.public.Start(); err != nil {
		return fmt.Errorf("start public server: %w", err)
	}
	if err := a.internal.Start(); err != nil {
		// Stop the listener that already came up, so a failed startup leaves
		// no half-running process behind.
		_ = a.public.Shutdown(context.Background())
		return fmt.Errorf("start internal server: %w", err)
	}

	<-ctx.Done()
	a.log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
	defer cancel()

	// Stop accepting public traffic first, then operational endpoints, so the
	// readiness probe keeps answering while requests drain.
	publicErr := a.public.Shutdown(shutdownCtx)
	internalErr := a.internal.Shutdown(shutdownCtx)

	if publicErr != nil {
		return fmt.Errorf("shut down public server: %w", publicErr)
	}
	if internalErr != nil {
		return fmt.Errorf("shut down internal server: %w", internalErr)
	}

	a.log.Info("core api stopped")
	return nil
}

// close releases resources in reverse assembly order, once.
func (a *App) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// ProbeURL derives the liveness URL for the container self-check from the
// internal listener address. The empty-address fallback is the same constant
// config.Load applies, so the healthcheck cannot drift from the real default.
func ProbeURL(internalAddr string) string {
	addr := internalAddr
	if addr == "" {
		addr = config.DefaultInternalAddr
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr + "/healthz"
}
