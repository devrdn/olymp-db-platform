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
	"path/filepath"
	"strings"
	"sync"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/gamedb"
	"github.com/devrdn/db-contest/backend/internal/gamefile"
	"github.com/devrdn/db-contest/backend/internal/health"
	"github.com/devrdn/db-contest/backend/internal/leaderboard"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/config"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
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

	// Moved ahead of the provisioning block below so the reclaim sweep can be
	// built WithAudit: an organizer who cannot find a database learns from
	// this trail what removed it, and the recorder needs to exist before
	// anything can be handed it.
	auditRecorder := audit.New(postgres.NewAuditSink(pool))

	// The SQL console, when there is a game cluster and a runner to reach.
	var console *queryproxy.Service
	// The game's authoring half — the script and the build. Nil in a
	// deployment with no game cluster configured, where there is nothing to
	// build a template on; the endpoints are then not mounted, the same way
	// the console's are not.
	var gameAuthoring *provisioning.Games
	// The pool half of the same screen: the databases that already exist for
	// a contest, and dropping one that has gone wrong. Nil in the same
	// deployments gameAuthoring is nil in — both are built inside the block
	// below, so the handler never sees one without the other.
	var gameDatabases *provisioning.Service

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
			// The same GAME_CLUSTER_MAX_BYTES the pool's own limits carry
			// below, and deliberately the same variable read once: the budget
			// bounds the background tender and the participant's own late
			// registration, and two numbers here would mean a cluster the pool
			// stopped filling at while participants carried on filling it
			// (provisioning.Service.roomForOneCopy).
			WithClusterBudget(cfg.ClusterMaxBytes).
			WithAudit(auditRecorder, storage.NewUnitOfWork(pool))
		a.tasks = append(a.tasks, tendPools(log, databases, provisioning.PoolLimits{
			Headroom: cfg.PoolDepth, MaxCopies: cfg.PoolMax, MaxClusterBytes: cfg.ClusterMaxBytes,
		}))
		gameDatabases = databases

		// The other half of a contest's game: the script an organiser writes
		// and the template built from it. `games` is the same
		// postgres.GameInstances the pool uses — one table, two jobs — and
		// `cluster` the same provisioner.
		gameAuthoring = provisioning.NewGames(games, cluster, games).
			WithAudit(auditRecorder, storage.NewUnitOfWork(pool))
		// The same GAME_BUILD_TIMEOUT the provisioner above was given: it is
		// what bounds a build, so it is also what decides when a build that has
		// not finished can only be a dead one (staleBuildAfter).
		a.tasks = append(a.tasks, buildGames(log, gameAuthoring.Build, cfg.GameBuildTimeout))

		// The second way to build a contest's game: upload a finished dump
		// instead of writing one in the editor. Optional in exactly the way
		// the console below is — GAME_UPLOAD_DIR empty means no volume was
		// mounted for it, and gameAuthoring simply never gets WithUploads,
		// which is what every upload method's ErrUploadsDisabled answers.
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

			// The table builder's own per-table CSV data (feat/game-table-builder's
			// third task): a second, independent gamefile.Store — never the one
			// uploads above uses (Games.WithTableData's own doc explains why one
			// store per directory matters here). Its own subdirectory, not a
			// sibling one: deploy/docker-compose.yml mounts the volume at exactly
			// GAME_UPLOAD_DIR, so anywhere outside it is the container's own
			// ephemeral disk, not the persistent volume both stores are meant to
			// share.
			//
			// MaxFileBytes and MaxChunkBytes are still the dump's own — one CSV
			// file's size and one chunk's size are each bounded per upload, and
			// nothing about a table's CSV needs a different ceiling for either.
			// MaxDirBytes is not shared: usage() (gamefile.Store's own accounting)
			// walks one directory with os.ReadDir, so it already can't see what
			// the other store keeps, but two Stores each independently allowed
			// the same GAME_UPLOAD_MAX_DIR_BYTES would together fit twice what an
			// operator who sized that variable against the volume itself meant to
			// allow — the defect this table's own tableLimits fixes, with its own
			// ceiling (GAME_UPLOAD_TABLE_MAX_DIR_BYTES, config.go's own doc) an
			// operator sizes separately, against the same volume, alongside it.
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
		// The background half of §2.4: a contest's participant databases
		// outlive it by exactly its configured grace, never longer, and
		// never a moment less. GameReclaimCounters degrades to a no-op on
		// whatever METRICS_BACKEND is not Prometheus — see its own doc.
		a.tasks = append(a.tasks, reclaimInstances(
			log, databases.Reclaim, cfg.GameInstanceGraceMin, metrics.NewGameReclaimCounters(recorder)))

		// The console needs a Query Runner to talk to. Without one the rest of
		// provisioning still works — pools are kept stocked — and the endpoint
		// simply is not mounted, which is the honest state of a deployment
		// where the runner has not been rolled out yet.
		if cfg.QueryRunnerAddr != "" {
			// Configuration refuses a missing token outside development; in
			// development it is allowed, and said out loud so that a stack
			// which lost its token cannot pass for a working one.
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

			console = queryproxy.New(
				postgres.NewRegistrations(pool),
				postgres.NewContests(pool),
				games,
				databases,
				queryrunner.NewJournalled(client, postgres.NewQueryLog(pool), log),
			).WithPerMinuteDefault(cfg.QueryPerMinute).WithGrace(cfg.DeadlineGrace).
				// The console's schema panel. Wired here and only here: the
				// console-less Service built further down for the participant
				// read endpoints has no game cluster to read a schema from,
				// and answers ErrSchemaHidden rather than pretending to.
				//
				// `games` is both halves of the cache — it is the same
				// postgres.GameInstances the pool tender already holds — and
				// `cluster` is the game cluster itself. One catalogue read
				// per template between every participant of a contest; see
				// provisioning.SchemaReader.
				WithSchemas(provisioning.NewSchemaReader(games, cluster)).
				// The console's own closing rule: once no question of the
				// contest is still answerable to a participant — every one
				// answered correctly or out of attempts — running a query
				// cannot lead to an answer, so Run stops taking them
				// (queryproxy.ErrNothingLeftToAnswer). Wired only here,
				// because only Run consults it: the read endpoints below
				// share this Service and are deliberately left open, so a
				// participant with nothing left to answer still has their
				// story, their results and their timer.
				WithAnswerable(postgres.NewAnswerable(pool))
		}
	}

	userRepo := postgres.NewUsers(pool)
	// The one reader of the audit trail, shared by the scheduler's own
	// dedup check (finding 1, below) and the admin-facing handler further
	// down: both read the same table through the same narrow type, and there
	// is no reason to pay for two.
	auditTrail := postgres.NewAuditTrail(pool)
	sessions := auth.NewSessionStore(cacheBackend, cfg.SessionTTL).WithMaxLifetime(cfg.SessionMaxLifetime)
	cookies := auth.NewCookieWriter(cfg.CookieSecure)

	// One instance, shared with GameHandler's own BeginUpload throttle
	// below: a subject string is namespaced by whoever builds it
	// ("game_upload_begin:" there, "ip:"/accountSubject here), so one cache
	// and one counter type serve every fixed-window rate limit this service
	// keeps rather than each feature growing its own.
	limiter := auth.NewLimiter(cacheBackend)

	// The process's one password hasher. Every argon2id computation holds
	// 64 MiB, so sign-in, password changes and account management share one
	// bound on how many run at once — two hashers would be two bounds, and
	// the memory limit in the deployment is sized against exactly one.
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
		Users:                 userRepo,
		Sessions:              sessions,
		Audit:                 auditRecorder,
		Limiter:               limiter,
		Logger:                log,
		Passwords:             passwords,
		MaxAttemptsPerAddress: cfg.MaxLoginAttemptsPerAddress,
		MaxAttemptsPerAccount: cfg.MaxLoginAttemptsPerAccount,
		Devices:               devices,
		MaxAttemptsPerDevice:  cfg.MaxLoginAttemptsPerDevice,
		// Every trusted browser of one account together.
		MaxTrustedAttemptsPerAccount: cfg.MaxTrustedLoginAttemptsPerAccount,
	})
	authMiddleware := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions:   sessions,
		Users:      userRepo,
		Authorizer: rbac.New(postgres.NewContestRoles(pool)),
		Cookies:    cookies,
		Logger:     log,
	})
	userService := users.NewService(userRepo, auditRecorder, storage.NewUnitOfWork(pool), passwords)

	// The game script the contest package carries (contests.GameSource,
	// Service.ExportPackage). Assigned through a declared interface variable
	// rather than handed the pointer directly: gameAuthoring is nil in a
	// deployment with no game cluster, and a nil *provisioning.Games stored
	// in an interface field is not a nil interface — the export would take
	// the "there is a game to read" branch and dereference it. Left nil here
	// instead, the package simply carries no game, which is the honest state
	// of such an installation.
	var packageGames contests.GameSource
	if gameAuthoring != nil {
		packageGames = gameAuthoring
	}

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
		// Read only by Service.ExportPackage, and nil where this deployment
		// has no game cluster at all — see packageGames above.
		Game: packageGames,
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
		// The same number reclaimInstances below hands the reclaim sweep
		// (finding 1): Service.ExtendGrace has to compare an organizer's
		// requested grace against the grace actually in force, and for a
		// contest that never set one explicitly that is this installation
		// default, not zero.
		DefaultGraceMin: cfg.GameInstanceGraceMin,
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
	// after publication. auditTrail is what lets it record a block once
	// rather than once a tick: the same reader the admin handler uses below.
	scheduler := contests.NewScheduler(
		postgres.NewContests(pool),
		postgres.NewStories(pool),
		postgres.NewQuestions(pool),
		auditTrail,
		auditRecorder,
		storage.NewUnitOfWork(pool),
		// The same grace Submit and queryproxy.Admitted add to a
		// participant's own deadline (cfg.DeadlineGrace, one grace for the
		// whole installation, §8), so the scheduler's own finish check never
		// closes a contest a tick before a late-arriving answer or query
		// inside that grace would still be admitted.
		cfg.DeadlineGrace,
	)
	a.tasks = append(a.tasks, advanceContestSchedule(log, scheduler.Advance))

	modules := []api.Module{
		api.NewAuthHandler(authService, userService, userRepo, authMiddleware, cookies, log),
		api.NewUsersHandler(userService, userRepo, authService, authMiddleware, log),
		api.NewSettingsHandler(
			settings.NewService(postgres.NewSettings(pool), postgres.NewSettingsImages(pool), auditRecorder, storage.NewUnitOfWork(pool)),
			authMiddleware, log),
		api.NewContestsHandler(contestService, authMiddleware, log, cfg.DefaultLocale),
		// The contest's table for its staff, its participants and anybody
		// with the link. Its own service rather than a corner of
		// contestService: it records nothing but a reveal, it caches, and
		// one of its routes is deliberately outside authentication.
		api.NewLeaderboardHandler(leaderboard.NewService(leaderboard.Config{
			Contests:     postgres.NewContests(pool),
			Participants: postgres.NewRegistrations(pool),
			Standings:    postgres.NewLeaderboard(pool),
			Audit:        auditRecorder,
			UnitOfWork:   storage.NewUnitOfWork(pool),
		}), limiter, authMiddleware, log, cfg.DefaultLocale),
		// The trail is written by every module above; this is the only way
		// to read it back, and it is behind its own permission.
		api.NewAuditHandler(auditTrail, authMiddleware, log),
	}
	if console != nil {
		modules = append(modules, api.NewConsoleHandler(console, authMiddleware, log))
	}
	// Mounted only where a game cluster is configured: without one there is
	// nothing to build a template on, and an endpoint that took a script it
	// could never build would be a worse answer than no endpoint.
	if gameAuthoring != nil {
		gameHandler := api.NewGameHandler(gameAuthoring, gameDatabases, authMiddleware, log, limiter)
		if cfg.GameUploadChunkBytes > 0 {
			// The socket's ceiling and the domain's are the same number, so
			// the configured chunk size is the only one that ever decides.
			gameHandler = gameHandler.WithMaxChunkBody(cfg.GameUploadChunkBytes)
			// The table builder's own CSV chunk shares this installation's
			// GAME_UPLOAD_CHUNK_BYTES too — WithTableData's own call above
			// configures provisioning.Games identically, so the socket's
			// ceiling for this second, independent store matches its
			// domain-side one the same way.
			gameHandler = gameHandler.WithMaxTableChunkBody(cfg.GameUploadChunkBytes)
		}
		modules = append(modules, gameHandler)
	}

	// The participant's own read of a running contest — the story, the
	// visible questions, and their own query log — needs
	// queryproxy.Service.Access and .AdmitRead, and neither ever reaches a
	// game lookup, provisioning or the Query Runner:
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
	// The same postgres.QueryLog the background sweep and (when there is a
	// console) the journal already use — one type serving both directions of
	// one table, not a second repository over it.
	history := postgres.NewQueryLog(pool)
	// Submit is the same contestService every staff endpoint above already
	// uses — not a second implementation of the answering rules, and not a
	// second Submissions repository either.
	// The answer throttle shares the one fixed-window limiter every other
	// counter in this service uses, under its own "answer:" namespace.
	answers := api.AnswerRate{Limiter: limiter, PerMinute: cfg.AnswerRatePerMinute}
	modules = append(modules, api.NewParticipantHandler(participantAccess, reader, history, contestService, answers, authMiddleware, log, cfg.DefaultLocale))
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
		Logger:        log,
		Metrics:       recorder,
		Version:       version,
		ClientIPs:     resolver,
		PublicOrigins: cfg.PublicOrigins,
		CacheMode:     cache.Mode(cacheBackend),
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
