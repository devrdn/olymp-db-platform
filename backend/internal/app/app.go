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
	"sync"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// App is the assembled service: two HTTP listeners over one dependency graph.
type App struct {
	cfg      config.Config
	log      *slog.Logger
	public   *server.Server
	internal *server.Server

	// tasks are the periodic jobs, started with Run and stopped with it.
	tasks []task

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
	// The second half of the two-phase query journal. A row is written before
	// its query runs so that a process dying mid-query leaves evidence; this
	// is what closes the evidence, and without it the guarantee is only
	// half-built (section 5, point 7).
	a.tasks = append(a.tasks, sweepQueryLog(log, postgres.NewQueryLog(pool).SweepAbandoned))

	// The SQL console, when there is a game cluster and a runner to reach.
	var console *queryproxy.Service

	// Provisioning is optional: a deployment with no game cluster has nothing
	// to provision, and refusing to start would make the game circuit a
	// requirement for running an olympiad's registration.
	if cfg.GameProvisionerDSN != "" {
		// Its own statement timeout, not the core API's ten seconds: what this
		// pool runs is CREATE DATABASE … TEMPLATE, which takes as long as
		// copying the template takes, and the core timeout would fail
		// provisioning exactly for the contests large enough to need it.
		gamePool, err := storage.NewMaintenancePool(ctx, cfg.GameProvisionerDSN, provisionStatementTimeout)
		if err != nil {
			a.close()
			return nil, fmt.Errorf("connect to the game cluster: %w", err)
		}
		a.closers = append(a.closers, gamePool.Close)

		cluster, err := gamedb.NewProvisioner(gamePool, cfg.GameProvisionerDSN)
		if err != nil {
			a.close()
			return nil, err
		}
		if cluster, err = cluster.WithCopyStrategy(gamedb.CopyStrategy(cfg.CopyStrategy)); err != nil {
			a.close()
			return nil, err
		}
		games := postgres.NewGameInstances(pool)
		databases := provisioning.New(games, cluster).WithWorkers(cfg.ProvisionWorkers)
		a.tasks = append(a.tasks, tendPools(log, databases, cfg.PoolDepth))

		// The console needs a Query Runner to talk to. Without one the rest of
		// provisioning still works — pools are kept stocked — and the endpoint
		// simply is not mounted, which is the honest state of a deployment
		// where the runner has not been rolled out yet.
		if cfg.QueryRunnerAddr != "" {
			client, err := rpc.Dial(cfg.QueryRunnerAddr)
			if err != nil {
				a.close()
				return nil, err
			}
			a.closers = append(a.closers, func() { _ = client.Close() })

			console = queryproxy.New(
				postgres.NewRegistrations(pool),
				postgres.NewContests(pool),
				games,
				databases,
				queryrunner.NewJournalled(client, postgres.NewQueryLog(pool), log),
			).WithPerMinuteDefault(cfg.QueryPerMinute).WithGrace(cfg.DeadlineGrace)
		}
	}

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
		// Records answers (submission.go). The same grace as queryproxy's own
		// console (cfg.DeadlineGrace): §8 names one deadline formula and one
		// grace, not one per path.
		Submissions: postgres.NewSubmissions(pool),
		// Answers whether a question has opened yet in a sequential contest
		// (§6.1.1); only ever consulted when a contest turns that on.
		Sequence:   postgres.NewSequence(pool),
		Grace:      cfg.DeadlineGrace,
		Users:      userRepo,
		Audit:      auditRecorder,
		UnitOfWork: storage.NewUnitOfWork(pool),
		Logger:     log,
	})

	// The background half of §8: published → running → finished without an
	// organizer asking, one advisory-locked tick at a time
	// (internal/app/background.go's own doc for the interval). Second
	// postgres.Contests, Stories and Questions values rather than the ones
	// contestService already holds: all of them wrap the same pool and the
	// same tables, so this costs nothing beyond the structs themselves, and
	// Scheduler asks for narrower things than contestService's own
	// contests.Repository, contests.StoryRepository and
	// contests.QuestionRepository — easiest to see when it is handed its own
	// values instead of borrowing fields out of another component. The
	// story and question reads are what let the scheduler hold the same
	// publish gate Service.Transition holds before letting a contest reach
	// running (finding 1): the scheduler is a second door into that step,
	// and it must not open onto a contest whose story or questions vanished
	// after publication.
	scheduler := contests.NewScheduler(
		postgres.NewContests(pool),
		postgres.NewStories(pool),
		postgres.NewQuestions(pool),
		auditRecorder,
		storage.NewUnitOfWork(pool),
	)
	a.tasks = append(a.tasks, advanceContestSchedule(log, scheduler.Advance))

	modules := []api.Module{
		api.NewAuthHandler(authService, userService, userRepo, authMiddleware, cookies, log),
		api.NewUsersHandler(userService, userRepo, authMiddleware, log),
		api.NewSettingsHandler(
			settings.NewService(postgres.NewSettings(pool), postgres.NewSettingsImages(pool), auditRecorder, storage.NewUnitOfWork(pool)),
			authMiddleware, log),
		api.NewContestsHandler(contestService, authMiddleware, log, cfg.DefaultLocale),
		// The trail is written by every module above; this is the only way
		// to read it back, and it is behind its own permission.
		api.NewAuditHandler(postgres.NewAuditTrail(pool), authMiddleware, log),
	}
	if console != nil {
		modules = append(modules, api.NewConsoleHandler(console, authMiddleware, log))
	}

	// The participant's own read of a running contest — the story and the
	// visible questions — needs queryproxy.Service.Access and .AdmitRead, and
	// neither ever reaches a game lookup, provisioning or the Query Runner:
	// those are Run's alone. So this is mounted unconditionally rather than
	// under "is there a game circuit at all" — a deployment with no game
	// cluster still runs an olympiad's registration and its participants
	// still have a story to read, and gating it on the console would give
	// them a 404 on their own contest for a dependency this endpoint does not
	// have.
	//
	// When there is a console, its Service is reused outright rather than a
	// second one built here: AdmitRead shares Run's own rate limiter and key
	// namespace (queryproxy.Service.AdmitRead), so a participant who
	// alternates between running queries and polling this endpoint spends one
	// account-wide budget, not two that add together past the installation's
	// intended rate. Without a console there is no Run to share a budget
	// with, so a fresh Service is built from the same two repositories the
	// console would have used (People, Contests) and nils for the three
	// collaborators only Run calls (Games, Databases, Executor) — Access and
	// AdmitRead never touch them.
	participantAccess := console
	if participantAccess == nil {
		participantAccess = queryproxy.New(
			postgres.NewRegistrations(pool), postgres.NewContests(pool),
			nil, nil, nil,
		).WithPerMinuteDefault(cfg.QueryPerMinute).WithGrace(cfg.DeadlineGrace)
	}
	// Sequence is a second instance of the same postgres.Sequence contestService
	// already holds one of (both are pool-backed, stateless readers): Reader
	// and Service sit in different packages and neither imports the other's
	// wiring, so each is handed its own rather than the two sharing a field
	// that would have to cross that boundary.
	reader := contests.NewReader(
		postgres.NewStories(pool), postgres.NewQuestions(pool), postgres.NewAttempts(pool), postgres.NewSequence(pool))
	// Submit is the same contestService every staff endpoint above already
	// uses — not a second implementation of the answering rules, and not a
	// second Submissions repository either.
	modules = append(modules, api.NewParticipantHandler(participantAccess, reader, contestService, authMiddleware, log, cfg.DefaultLocale))
	// The SSE channel (§8) shares participantAccess with the endpoints above
	// for the same reason: one Access, one AdmitRead budget, not a second
	// admission decision that could drift from the first. ctx.Done() is the
	// same context main.go cancels on SIGINT/SIGTERM and Run waits on before
	// draining the servers — passed here, not derived fresh, so an open
	// connection's next select sees the shutdown at the same instant Run
	// starts one, rather than after whatever this constructor happened to do
	// with a context of its own (see EventsHandler's own doc).
	modules = append(modules, api.NewEventsHandler(participantAccess, authMiddleware, log, ctx.Done()))

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
		Modules: modules,
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

	// Background jobs get their own cancellation so that shutdown stops them
	// before the listeners drain: a job writing to the database while the pool
	// is being closed is a confusing error in the log of an orderly shutdown.
	jobs, stopJobs := context.WithCancel(ctx)
	var running sync.WaitGroup
	for _, t := range a.tasks {
		running.Add(1)
		go func() {
			defer running.Done()
			runPeriodically(jobs, a.log, t)
		}()
	}

	<-ctx.Done()
	a.log.Info("shutdown signal received")

	stopJobs()
	running.Wait()

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
