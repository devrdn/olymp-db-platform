// Package app is the composition root: it assembles the Core API from its
// parts, runs its listeners and background jobs, and shuts them down. It is
// the only package that knows about all the others; main keeps only flags and
// exit codes.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/filestore"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/server"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/profile"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/queryproxy"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/rpc"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/showcase"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/workspace"
)

// App is the assembled service: two HTTP listeners over one dependency graph.
type App struct {
	cfg      config.Config
	log      *slog.Logger
	public   *server.Server
	internal *server.Server

	tasks []task

	// closers run in reverse assembly order on shutdown.
	closers []func()
}

// New builds the service, failing fast on any unusable dependency or setting
// before a request is accepted.
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

	pool, err := storage.NewPoolWithMaxConns(ctx, cfg.CoreDBDSN, int32(cfg.CoreDBPoolMax)) // #nosec G115 -- config.Load bounds CoreDBPoolMax to [0, maxCoreDBPoolMax], well within int32.
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, pool.Close)

	cacheBackend, err := cache.New(ctx, cfg.RedisAddr, log)
	if err != nil {
		return nil, err
	}
	a.closers = append(a.closers, func() { _ = cacheBackend.Close() })

	recorder, err := metrics.New(cfg.MetricsBackend, log)
	if err != nil {
		return nil, fmt.Errorf("configure metrics: %w", err)
	}
	log.Info("metrics backend ready", "backend", cfg.MetricsBackend)

	if runner, ok := recorder.(metrics.Runner); ok {
		stop := make(chan struct{})
		a.closers = append(a.closers, func() { close(stop) })
		go runner.Run(stop)
	}

	// Forwarded headers are believed only from configured proxies (CLAUDE.md
	// rule 9).
	resolver, err := httpx.NewIPResolver(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("configure trusted proxies: %w", err)
	}

	a.tasks = append(a.tasks, sweepQueryLog(log, postgres.NewQueryLog(pool).SweepAbandoned))

	// Built before provisioning so the reclaim sweep can record what it drops.
	auditRecorder := audit.New(postgres.NewAuditSink(pool))

	// The session store comes first: the monitoring tracker asks it whether a
	// registration's previous session still lives before calling a new one a
	// second device.
	sessions := auth.NewSessionStore(cacheBackend, cfg.SessionTTL).WithMaxLifetime(cfg.SessionMaxLifetime)
	participantTracker := monitor.NewTracker(cacheBackend, postgres.NewMonitor(pool), sessions, log)

	// The participation gate, built once and handed to the console, the
	// answer route, the scheduler and the profile, so none of them can
	// disagree about when a participant's time is up.
	gate := contests.NewGate(cfg.DeadlineGrace)

	var console *queryproxy.Service
	// console, gameAuthoring and gameDatabases stay nil without a game
	// cluster, and their endpoints are then not mounted.
	var gameAuthoring *provisioning.Games
	var gameDatabases *provisioning.Service
	// poolTrigger wakes tendPools early when a contest starts or its roster
	// grows. It stays a true nil interface without a game cluster: a typed
	// nil pointer in an interface is not nil.
	var poolTrigger contests.PoolTrigger

	// Provisioning is optional, so registration runs without a game cluster.
	if cfg.GameProvisionerDSN != "" {
		// Its own statement timeout: CREATE DATABASE … TEMPLATE outlasts the
		// core API's ten seconds (CLAUDE.md rule 15).
		gamePool, err := storage.NewMaintenancePool(ctx, cfg.GameProvisionerDSN, provisionStatementTimeout)
		if err != nil {
			a.close()
			return nil, fmt.Errorf("connect to the game cluster: %w", err)
		}
		a.closers = append(a.closers, gamePool.Close)

		cluster, err := gamedb.NewProvisioner(gamePool, cfg.GameProvisionerDSN, cfg.GameAuthorPassword)
		if err != nil {
			a.close()
			return nil, err
		}
		if cluster, err = cluster.WithCopyStrategy(gamedb.CopyStrategy(cfg.CopyStrategy)); err != nil {
			a.close()
			return nil, err
		}
		if cluster, err = cluster.WithBuildTimeout(cfg.GameBuildTimeout); err != nil {
			a.close()
			return nil, err
		}
		games := postgres.NewGameInstances(pool)
		databases := provisioning.New(games, cluster).
			WithWorkers(cfg.ProvisionWorkers).
			// The same budget as the tender's below, so late registrations
			// cannot keep filling a cluster the tender stopped at.
			WithClusterBudget(cfg.ClusterMaxBytes).
			WithAudit(auditRecorder, storage.NewUnitOfWork(pool))
		// Coalesces a burst of triggers into one extra tend.
		tender := provisioning.NewTender()
		poolTrigger = tender
		a.tasks = append(a.tasks, tendPools(log, databases, provisioning.PoolLimits{
			Headroom: cfg.PoolDepth, MaxCopies: cfg.PoolMax, MaxClusterBytes: cfg.ClusterMaxBytes,
		}, tender))
		gameDatabases = databases

		gameAuthoring = provisioning.NewGames(games, cluster, games).
			WithAudit(auditRecorder, storage.NewUnitOfWork(pool))
		// The build timeout also decides when an unfinished build is dead.
		a.tasks = append(a.tasks, buildGames(log, gameAuthoring.Build, cfg.GameBuildTimeout))

		// Dump uploads are off without GAME_UPLOAD_DIR (ErrUploadsDisabled).
		if cfg.GameUploadDir != "" {
			limits := gamefile.Limits{
				MaxFileBytes:  cfg.GameUploadMaxFileBytes,
				MaxDirBytes:   cfg.GameUploadMaxDirBytes,
				MaxChunkBytes: cfg.GameUploadChunkBytes,
			}
			uploads, err := gamefile.NewStore(cfg.GameUploadDir, limits)
			if err != nil {
				a.close()
				return nil, fmt.Errorf("open the upload directory: %w", err)
			}
			gameAuthoring = gameAuthoring.WithUploads(uploads, limits)

			// The table builder's CSV data gets its own store in a
			// subdirectory: inside GAME_UPLOAD_DIR because only that path is
			// the mounted volume. File and chunk limits are the dump's; the
			// directory budget is its own, so the two stores together do not
			// double what the operator sized.
			tableDir := filepath.Join(cfg.GameUploadDir, "tables")
			tableLimits := gamefile.Limits{
				MaxFileBytes:  cfg.GameUploadMaxFileBytes,
				MaxDirBytes:   cfg.GameUploadTableMaxDirBytes,
				MaxChunkBytes: cfg.GameUploadChunkBytes,
			}
			tableFiles, err := gamefile.NewStore(tableDir, tableLimits)
			if err != nil {
				a.close()
				return nil, fmt.Errorf("open the table data directory: %w", err)
			}
			gameAuthoring = gameAuthoring.WithTableData(tableFiles, tableLimits)

			a.tasks = append(a.tasks, abandonedUploads(log, gameAuthoring, cfg.GameUploadAbandonedAfter))
		}
		// Participant databases are dropped once their contest's grace period
		// has passed, never before.
		a.tasks = append(a.tasks, reclaimInstances(
			log, databases.Reclaim, cfg.GameInstanceGraceMin, metrics.NewGameReclaimCounters(recorder)))

		// Without a Query Runner, pools are still stocked but the console is
		// not mounted.
		if cfg.QueryRunnerAddr != "" {
			// Allowed only in development, and logged loudly.
			if cfg.QueryRunnerToken == "" {
				log.Warn("QUERY_RUNNER_TOKEN is not set: calls to the query runner carry no token, " +
					"which only a development runner accepts")
			}
			client, err := rpc.Dial(cfg.QueryRunnerAddr, cfg.QueryRunnerToken)
			if err != nil {
				a.close()
				return nil, err
			}
			a.closers = append(a.closers, func() { _ = client.Close() })

			consoleRegistrations := postgres.NewRegistrations(pool)
			console = queryproxy.New(
				consoleRegistrations,
				postgres.NewContests(pool),
				games,
				databases,
				queryrunner.NewJournalled(client, postgres.NewQueryLog(pool), log),
				gate,
			).WithPerMinuteDefault(cfg.QueryPerMinute).
				// Collapses Run's and Access's lookups into one round trip.
				WithLookup(consoleRegistrations).
				// The schema panel; a console-less Service answers
				// ErrSchemaHidden.
				WithSchemas(provisioning.NewSchemaReader(games, cluster)).
				// Run refuses once nothing is left to answer
				// (ErrNothingLeftToAnswer); the read endpoints stay open.
				WithAnswerable(postgres.NewAnswerable(pool)).
				WithWatcher(participantTracker)
		}
	}

	userRepo := postgres.NewUsers(pool)
	auditTrail := postgres.NewAuditTrail(pool)
	cookies := auth.NewCookieWriter(cfg.CookieSecure)

	// One limiter serves every fixed-window rate limit; each caller
	// namespaces its own subjects.
	limiter := auth.NewLimiter(cacheBackend)

	// The process's one password hasher: the memory limit is sized against
	// exactly one bound on concurrent argon2id runs.
	passwords := password.NewHasher(password.HasherConfig{
		Concurrency: cfg.PasswordHashConcurrency,
		MaxWait:     cfg.PasswordHashMaxWait,
	})
	checkMemoryLimit(log, passwords.Concurrency())

	devices, err := auth.NewDeviceTrust(cfg.DeviceCookieSecret, cfg.DeviceCookieTTL)
	if err != nil {
		return nil, fmt.Errorf("device trust: %w", err)
	}
	authService := auth.NewService(auth.ServiceConfig{
		Users:                        userRepo,
		Sessions:                     sessions,
		Audit:                        auditRecorder,
		Limiter:                      limiter,
		Logger:                       log,
		Passwords:                    passwords,
		MaxAttemptsPerAddress:        cfg.MaxLoginAttemptsPerAddress,
		MaxAttemptsPerAccount:        cfg.MaxLoginAttemptsPerAccount,
		Devices:                      devices,
		MaxAttemptsPerDevice:         cfg.MaxLoginAttemptsPerDevice,
		MaxTrustedAttemptsPerAccount: cfg.MaxTrustedLoginAttemptsPerAccount,
	})
	// The account behind a session, cached briefly so authentication is a
	// cache read. The user service invalidates it on every change, so a block
	// applies on the next request. A zero TTL turns it off.
	var accounts *auth.AccountCache
	if cfg.SessionAccountCacheTTL > 0 {
		accounts = auth.NewAccountCache(cacheBackend, cfg.SessionAccountCacheTTL, log)
	}
	authMiddleware := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions:   sessions,
		Users:      userRepo,
		Accounts:   accounts,
		Authorizer: rbac.New(postgres.NewContestRoles(pool)),
		Cookies:    cookies,
		Logger:     log,
	})
	userService := users.NewService(userRepo, auditRecorder, storage.NewUnitOfWork(pool), passwords)
	if accounts != nil {
		// Guarded: a nil *AccountCache in an interface is not a nil interface.
		userService.WithAccessCache(accounts)
	}

	// Assigned only when gameAuthoring exists: a nil *provisioning.Games in
	// the interface would not be nil, and ExportPackage would dereference it.
	var packageGames contests.GameSource
	if gameAuthoring != nil {
		packageGames = gameAuthoring
	}

	contestService := contests.NewService(contests.ServiceConfig{
		Contests:      postgres.NewContests(pool),
		Stories:       postgres.NewStories(pool),
		Questions:     postgres.NewQuestions(pool),
		Managers:      postgres.NewContestManagers(pool),
		Registrations: postgres.NewRegistrations(pool),
		Policies:      postgres.NewSQLPolicies(pool),
		Languages:     postgres.NewLanguages(pool),
		Game:          packageGames,
		Submissions:   postgres.NewSubmissions(pool),
		Sequence:      postgres.NewSequence(pool),
		Gate:          gate,
		Users:         userRepo,
		Audit:         auditRecorder,
		UnitOfWork:    storage.NewUnitOfWork(pool),
		Logger:        log,
		// The same default the reclaim sweep uses, so ExtendGrace compares
		// against the grace actually in force.
		DefaultGraceMin: cfg.GameInstanceGraceMin,
		PoolTrigger:     poolTrigger,
	})

	// The scheduler moves contests published → running → finished. It holds
	// the same publish gate as Service.Transition before starting a contest,
	// and reads the audit trail to record a blocked start once, not per tick.
	scheduler := contests.NewScheduler(
		postgres.NewContests(pool),
		postgres.NewStories(pool),
		postgres.NewQuestions(pool),
		postgres.NewRegistrations(pool),
		auditTrail,
		auditRecorder,
		storage.NewUnitOfWork(pool),
		// The same gate, so a contest never finishes while a late answer
		// inside the grace would still be admitted.
		gate,
	).WithPoolTrigger(poolTrigger).WithCovers(postgres.NewCovers(pool))
	a.tasks = append(a.tasks, advanceContestSchedule(log, scheduler.Advance))

	// One leaderboard service for the table and the profile, so a report and
	// the table can never disagree.
	standings := leaderboard.NewService(leaderboard.Config{
		Contests:     postgres.NewContests(pool),
		Participants: postgres.NewRegistrations(pool),
		Standings:    postgres.NewLeaderboard(pool),
		Audit:        auditRecorder,
		UnitOfWork:   storage.NewUnitOfWork(pool),
	})
	// One WatchService for staff monitoring and the participant's profile.
	watch := monitor.NewWatchService(monitor.WatchConfig{
		Store: postgres.NewWatch(pool), Audit: auditRecorder, Marks: cacheBackend,
	})

	// One bound on CSV downloads holding pool connections, shared by every
	// export handler since they draw on one pool.
	exportSlots := api.NewExportSlots(cfg.ExportConcurrency)

	modules := []api.Module{
		api.NewAuthHandler(authService, userService, userRepo, authMiddleware, cookies, log),
		api.NewUsersHandler(userService, userRepo, authService, authMiddleware, log),
		api.NewSettingsHandler(
			settings.NewService(postgres.NewSettings(pool), postgres.NewSettingsImages(pool), auditRecorder, storage.NewUnitOfWork(pool)),
			authMiddleware, log),
		api.NewContestsHandler(contestService, authMiddleware, log, cfg.DefaultLocale),
		api.NewLeaderboardHandler(standings, limiter, authMiddleware, log, cfg.DefaultLocale),
		api.NewAuditHandler(auditTrail, authMiddleware, log),
		api.NewMonitorHandler(watch, limiter, authMiddleware, log).WithExportSlots(exportSlots),
	}
	if console != nil {
		modules = append(modules, api.NewConsoleHandler(console, authMiddleware, log))
	}
	if gameAuthoring != nil {
		gameHandler := api.NewGameHandler(gameAuthoring, gameDatabases, authMiddleware, log, limiter)
		if cfg.GameUploadChunkBytes > 0 {
			// The socket's ceiling equals the domain's chunk limit.
			gameHandler = gameHandler.WithMaxChunkBody(cfg.GameUploadChunkBytes)
			gameHandler = gameHandler.WithMaxTableChunkBody(cfg.GameUploadChunkBytes)
		}
		modules = append(modules, gameHandler)
	}

	// The participant's reads of a running contest use only Access and
	// AdmitRead, which never touch the game cluster, so they are mounted even
	// without a console. With a console its Service is reused, so queries and
	// reads spend one budget; without one, a Service is built with nils for
	// the collaborators only Run uses.
	participantAccess := console
	if participantAccess == nil {
		registrations := postgres.NewRegistrations(pool)
		participantAccess = queryproxy.New(
			registrations, postgres.NewContests(pool),
			nil, nil, nil,
			gate,
		).WithPerMinuteDefault(cfg.QueryPerMinute).
			WithLookup(registrations)
	}
	reader := contests.NewReader(
		postgres.NewStories(pool), postgres.NewQuestions(pool), postgres.NewAttempts(pool), postgres.NewSequence(pool))
	history := postgres.NewQueryLog(pool)
	answers := api.AnswerRate{Limiter: limiter, PerMinute: cfg.AnswerRatePerMinute}
	workspaces := workspace.NewService(postgres.NewWorkspace(pool), limiter)
	signals := monitor.NewSignals(limiter, postgres.NewMonitor(pool))
	// One export slot per registration across the play screen and profile
	// routes that serve its query log.
	logExports := api.NewExportGate()
	modules = append(modules, api.NewParticipantHandler(participantAccess, reader, history, contestService, answers, authMiddleware, log, cfg.DefaultLocale).
		WithWorkspace(workspaces).
		WithWatcher(participantTracker).
		WithSignals(signals).
		WithExports(logExports).
		WithExportSlots(exportSlots))
	// The SSE channel shares participantAccess and its budget. ctx.Done() is
	// the shutdown signal Run waits on, so open streams close as draining
	// begins.
	modules = append(modules, api.NewEventsHandler(participantAccess, authMiddleware, log, ctx.Done()))
	// The profile reuses standings, watch and the query log rather than
	// reimplementing them.
	modules = append(modules, api.NewProfileHandler(profile.NewService(profile.Config{
		Store:        postgres.NewProfile(pool),
		Contests:     postgres.NewContests(pool),
		Participants: postgres.NewRegistrations(pool),
		Results:      standings,
		Attempts:     watch,
		// Results open the instant the gate stops admitting the participant.
		Gate: gate,
	}), watch, history, limiter, authMiddleware, log, cfg.DefaultLocale).
		WithExports(logExports).
		WithExportSlots(exportSlots))
	// The landing page's reads, unauthenticated and rate-limited by address.
	modules = append(modules, api.NewPublicHandler(
		showcase.NewService(showcase.Config{Repository: postgres.NewShowcase(pool)}),
		limiter, log, cfg.DefaultLocale))

	// Covers are not optional: an unwritable directory fails startup, and the
	// same write probe answers /readyz, since a volume can turn read-only
	// later.
	coverFiles, err := filestore.New(cfg.CoverDir)
	if err != nil {
		a.close()
		return nil, fmt.Errorf("open the cover directory: %w", err)
	}
	log.Info("cover storage ready", "dir", coverFiles.Dir())

	modules = append(modules, api.NewCoverHandler(
		covers.NewService(postgres.NewCovers(pool), coverFiles),
		limiter, authMiddleware, log))

	deps := api.Deps{
		Logger:        log,
		Metrics:       recorder,
		Version:       version,
		ClientIPs:     resolver,
		PublicOrigins: cfg.PublicOrigins,
		CacheMode:     cache.Mode(cacheBackend),
		Checkers: []health.Checker{
			storage.NewChecker("core-db", pool),
			storage.NewChecker("cache", cacheBackend),
			storage.NewChecker("covers", coverFiles),
		},
		Modules: modules,
	}

	a.public = server.New("public", cfg.HTTPAddr, api.NewRouter(deps), log)
	a.internal = server.New("internal", cfg.InternalAddr, api.NewInternalRouter(deps), log)

	ok = true
	return a, nil
}

// Run serves until ctx is cancelled, then drains within the shutdown timeout.
// It always releases the app's resources before returning.
func (a *App) Run(ctx context.Context) error {
	defer a.close()

	if err := a.public.Start(); err != nil {
		return fmt.Errorf("start public server: %w", err)
	}
	if err := a.internal.Start(); err != nil {
		_ = a.public.Shutdown(context.Background())
		return fmt.Errorf("start internal server: %w", err)
	}

	// Background jobs stop before the listeners drain, so none writes while
	// the pool is closing.
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

	// Public listener first, so readiness keeps answering while requests
	// drain.
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

func (a *App) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// ProbeURL derives the liveness URL for the container self-check from the
// internal listener address, defaulting as config.Load does.
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
