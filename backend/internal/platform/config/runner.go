package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Runner is the Query Runner service's configuration.
//
// A type of its own rather than fields on Config, because the two binaries
// hold different things and neither should be able to read the other's. The
// Core API must not carry the game cluster's credentials — that separation is
// one of the two reasons the runner is a separate service at all — and a
// single struct with both would put them in the same process image whether
// they were used or not.
//
// The figures stay primitives here. internal/platform is infrastructure and
// must not import a domain package, so assembling queryrunner.Limits out of
// these belongs to the command that does the wiring.
type Runner struct {
	Env      string
	LogLevel string
	// ListenAddr is where the Core API reaches this service. It is never
	// published outside the private network, and defaults to loopback.
	ListenAddr string
	// Token is the shared secret every call must carry (QUERY_RUNNER_TOKEN):
	// this service runs whatever database and policy a request names, so it
	// answers only the Core API. Required outside development; never logged.
	Token string
	// GameDBDSN connects as the reading participant role — game_reader — and
	// is the only place in the system that holds it.
	GameDBDSN string
	// GameDBWriterDSN connects as game_writer, for contests whose policy
	// permits writing (section 4.1: which role is used is the policy's
	// decision). Optional: without it a read-write contest is refused rather
	// than quietly run as the reader, whose missing grants would turn every
	// permitted write into "permission denied".
	GameDBWriterDSN string
	ShutdownTimeout time.Duration

	// Deadline bounds one query, and is the bound that holds against SQL the
	// validator did not stop: statement_timeout is USERSET and can be turned
	// off from inside a query, a context deadline cannot.
	Deadline time.Duration
	MaxRows  int
	MaxBytes int
	// Concurrent is how many queries may run at once against the instance, and
	// QueueDepth how many may wait. Section 4.3 puts Concurrent at two to
	// three times the cores.
	//
	// QueueDepth is sized from the roster rather than from the cores. A
	// participant has at most one query in flight (queryrunner.ErrAlreadyRunning),
	// so Concurrent + QueueDepth at or above the number of participants means
	// a round in which everybody presses Run at once is queued rather than
	// refused. The default, 32, is that sum for the forty participants the
	// olympiad expects; `make loadtest` measures a deployment's own.
	Concurrent int
	QueueDepth int
	// PerMinute bounds how often one participant may ask. The semaphore
	// cannot: a thousand cheap queries pass it one at a time.
	PerMinute int
	// IdleConnTimeout is how long the connection a clean read finished on is
	// kept for that database's next query (QUERY_CONN_IDLE_TIMEOUT), sparing
	// the next query a connection handshake. Zero keeps none. Kept connections
	// count against Concurrent, so this changes how often the game cluster
	// pays for a handshake, never how many backends it holds.
	IdleConnTimeout time.Duration
	// ExtraFunctions are functions an operator has added to the allow-list
	// after a pilot, without waiting for a release (section 5, point 3).
	ExtraFunctions []string

	// GameDBMemoryBytes is the memory limit of the game cluster's container,
	// in bytes (GAME_DB_MEMORY_BYTES) — the single variable the compose file
	// interpolates the container's limit from, with the same default. It is
	// not used to run anything; it exists so the runner refuses to start with
	// a concurrency the cluster cannot hold. There is no "undeclared": an
	// absent or empty variable means the default-sized container, and the
	// check runs against that.
	GameDBMemoryBytes int64
	// ProcessMemoryBytes is the per-process memory cap the game cluster's
	// backends run under (GAME_DB_PROCESS_MEMORY_BYTES, the same value set as
	// ulimits.data on pg-game and verified in force by the deploy). It is the
	// one source of truth for the cap: the sizing check multiplies it, the
	// compose file interpolates the ulimit from it, and gamedb's deploy-time
	// self-check reads the running limit and compares it to it. Default is the
	// pilot's 256 MiB.
	ProcessMemoryBytes int64
}

// The game cluster's memory is the bound the whole admission-control story
// rests on, and these numbers turn QUERY_CONCURRENT into a demand on it.
//
// The game cluster's memory is the bound the whole admission-control story
// rests on, and these numbers turn QUERY_CONCURRENT into a demand on it. The
// per-process cap itself is not here — it is configuration (ProcessMemoryBytes,
// GAME_DB_PROCESS_MEMORY_BYTES), so the compose ulimit, the runner's arithmetic
// and the deploy-time self-check all read one value.
//
// MaxParallelWorkers is how many parallel worker processes the whole cluster
// runs at once — a shared pool, pinned with max_parallel_workers on the
// pg-game command, not a per-query number. Every one can reach the cap, so
// they are counted once against the container, not once per concurrent query.
// It must equal max_parallel_workers on the pg-game command; a test on the
// prepared test cluster (internal/gamedb) compares the two.
//
// MaxBuildSessions is how many game-cluster backends a provisioning build can
// occupy at the cap at once: an organiser's game-script session runs arbitrary
// SQL and can allocate as much as a participant's. It equals the game_author
// role's CONNECTION LIMIT (internal/gamedb.authorConnectionLimit), which a test
// on the prepared test cluster reads back from pg_roles and compares; the
// provisioner's own CREATE DATABASE / COPY sessions are lighter and left to the
// reserve. Counted because a build can coincide with a contest: an organiser
// publishing one game while participants query another.
//
// AutovacuumWorkers is how many autovacuum workers the reserve below allows
// for. It must equal autovacuum_max_workers on the pg-game command; a test on
// the prepared test cluster compares the two.
//
// ReservedMemoryBytes is what the cluster needs before the leaders, workers and
// build sessions: shared_buffers, the postmaster and its background workers,
// the /dev/shm parallel-query segment (256 MiB, container memory though not
// RLIMIT_DATA), the AutovacuumWorkers autovacuum workers (each bounded by
// maintenance_work_mem, 64 MiB by default) and the provisioner's own sessions. Two
// gibibytes is above the sum measured for the pilot's settings.
//
// So a container holds
//
//	(QUERY_CONCURRENT + MaxParallelWorkers + MaxBuildSessions) × ProcessMemoryBytes
//	+ ReservedMemoryBytes
//
// at worst, and GAME_DB_MEMORY_BYTES must be at least that. QUERY_CONCURRENT
// here is every participant backend the runner holds, not only the ones
// running a query: the connections it keeps idle between a participant's
// queries count against the same bound (queryrunner's pool closes the least
// recently used one before opening a connection past it), because a kept
// backend can still hold what its last query grew to. So the formula needs no
// term for them. At the default
// concurrency of eight, (8 + 4 + 4) × 256 MiB + 2 GiB = 6 GiB, which
// deploy/.env.example rounds up to a seven-gibibyte GAME_DB_MEMORY_BYTES. Lower one
// and the others can come down with it.
const (
	MaxParallelWorkers  = 4
	MaxBuildSessions    = 4
	AutovacuumWorkers   = 2
	ReservedMemoryBytes = 2 << 30

	// DefaultIdleConnTimeout and MaxIdleConnTimeout are QUERY_CONN_IDLE_TIMEOUT's
	// default and ceiling.
	DefaultIdleConnTimeout = 30 * time.Second
	MaxIdleConnTimeout     = 5 * time.Minute

	// DefaultProcessMemoryBytes is the pilot's per-process cap, used when
	// GAME_DB_PROCESS_MEMORY_BYTES is unset.
	DefaultProcessMemoryBytes = 256 << 20

	// DefaultGameDBMemoryBytes is the game cluster's container memory limit
	// when GAME_DB_MEMORY_BYTES is unset — the same default the compose file
	// interpolates the container limit from (7 GiB, 7516192768 bytes).
	// Exported because cmd/gamedb verifies the running container against the
	// same number at deploy.
	DefaultGameDBMemoryBytes = 7 << 30
)

// LoadRunner reads the Query Runner's configuration from the environment.
func LoadRunner() (Runner, error) {
	cfg := Runner{
		Env:      envOrDefault("ENV", "development"),
		LogLevel: envOrDefault("LOG_LEVEL", "info"),
		// Loopback unless told otherwise. This service runs the database and
		// policy a request names, so a runner started on a laptop must not be
		// reachable from the network it sits on; the compose file names ":9100"
		// explicitly, inside a network nothing outside routes into.
		ListenAddr: envOrDefault("QUERY_RUNNER_ADDR", "127.0.0.1:9100"),
	}

	var err error
	if cfg.Token, err = queryRunnerToken(cfg.Env, true); err != nil {
		return Runner{}, err
	}
	if cfg.GameDBDSN, err = requiredEnv("GAME_DB_DSN"); err != nil {
		return Runner{}, err
	}
	cfg.GameDBWriterDSN = os.Getenv("GAME_DB_WRITER_DSN")
	for name, value := range map[string]string{"GAME_DB_DSN": cfg.GameDBDSN, "GAME_DB_WRITER_DSN": cfg.GameDBWriterDSN} {
		if err := RefusePlaceholder(cfg.Env, name, value); err != nil {
			return Runner{}, err
		}
	}
	if !slices.Contains(validLogLevels, cfg.LogLevel) {
		return Runner{}, fmt.Errorf("LOG_LEVEL: unknown level %q, want one of %v", cfg.LogLevel, validLogLevels)
	}
	if cfg.ShutdownTimeout, err = durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		return Runner{}, err
	}
	if cfg.Deadline, err = durationEnv("QUERY_DEADLINE", 5*time.Second); err != nil {
		return Runner{}, err
	}
	if cfg.MaxRows, err = intEnv("QUERY_MAX_ROWS", 1000); err != nil {
		return Runner{}, err
	}
	if cfg.MaxBytes, err = intEnv("QUERY_MAX_BYTES", 5<<20); err != nil {
		return Runner{}, err
	}
	if cfg.Concurrent, err = intEnv("QUERY_CONCURRENT", 8); err != nil {
		return Runner{}, err
	}
	if cfg.QueueDepth, err = intEnv("QUERY_QUEUE_DEPTH", 32); err != nil {
		return Runner{}, err
	}
	if cfg.PerMinute, err = intEnv("QUERY_PER_MINUTE", 30); err != nil {
		return Runner{}, err
	}
	if cfg.IdleConnTimeout, err = durationEnv("QUERY_CONN_IDLE_TIMEOUT", DefaultIdleConnTimeout); err != nil {
		return Runner{}, err
	}
	// Zero is the way back to a connection per query. Past the ceiling a
	// backend stays on a database nobody has queried for minutes, which only
	// delays the reclaim sweep's plain DROP DATABASE and buys no handshake a
	// participant is waiting on.
	if cfg.IdleConnTimeout < 0 || cfg.IdleConnTimeout > MaxIdleConnTimeout {
		return Runner{}, fmt.Errorf("QUERY_CONN_IDLE_TIMEOUT must be between 0 and %s, got %s",
			MaxIdleConnTimeout, cfg.IdleConnTimeout)
	}
	if cfg.PerMinute < 0 {
		return Runner{}, fmt.Errorf("QUERY_PER_MINUTE cannot be negative, got %d", cfg.PerMinute)
	}
	// An allow-list an operator extends after a pilot without a release, which
	// is what section 5 asks for. Split eagerly so a stray comma fails the
	// boot rather than silently allowing a function called "".
	for _, name := range strings.Split(os.Getenv("QUERY_EXTRA_FUNCTIONS"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			cfg.ExtraFunctions = append(cfg.ExtraFunctions, name)
		}
	}

	if cfg.GameDBMemoryBytes, err = int64Env("GAME_DB_MEMORY_BYTES", DefaultGameDBMemoryBytes); err != nil {
		return Runner{}, err
	}
	if cfg.GameDBMemoryBytes < 1 {
		return Runner{}, fmt.Errorf("GAME_DB_MEMORY_BYTES must be at least 1, got %d", cfg.GameDBMemoryBytes)
	}
	if cfg.ProcessMemoryBytes, err = int64Env("GAME_DB_PROCESS_MEMORY_BYTES", DefaultProcessMemoryBytes); err != nil {
		return Runner{}, err
	}
	if cfg.ProcessMemoryBytes < 1 {
		return Runner{}, fmt.Errorf("GAME_DB_PROCESS_MEMORY_BYTES must be at least 1, got %d", cfg.ProcessMemoryBytes)
	}

	// Each of these turns a limit into no limit at all, and a zero from a
	// mistyped variable is exactly how that happens. Refusing at startup is
	// the difference between a misconfiguration and an olympiad that quietly
	// has no bounds.
	for name, value := range map[string]int{
		"QUERY_MAX_ROWS":   cfg.MaxRows,
		"QUERY_MAX_BYTES":  cfg.MaxBytes,
		"QUERY_CONCURRENT": cfg.Concurrent,
	} {
		if value < 1 {
			return Runner{}, fmt.Errorf("%s must be at least 1, got %d", name, value)
		}
	}
	if cfg.QueueDepth < 0 {
		return Runner{}, fmt.Errorf("QUERY_QUEUE_DEPTH cannot be negative, got %d", cfg.QueueDepth)
	}
	if cfg.Deadline <= 0 {
		return Runner{}, fmt.Errorf("QUERY_DEADLINE must be positive, got %s", cfg.Deadline)
	}
	// A concurrency the game cluster's memory cannot hold defeats the
	// per-process cap: the cap keeps one backend's failure to an "out of
	// memory" ERROR, but only while the container can hold every concurrent
	// backend at the cap at once — otherwise the cgroup limit is reached first
	// and the OOM killer restarts the cluster. Caught here, so it is a failed
	// deploy and not a discovery during a contest. Always checked: the limit
	// has a default, the same one the container is sized from.
	processes := int64(cfg.Concurrent) + int64(MaxParallelWorkers) + int64(MaxBuildSessions)
	need := processes*cfg.ProcessMemoryBytes + ReservedMemoryBytes
	if need > cfg.GameDBMemoryBytes {
		return Runner{}, fmt.Errorf(
			"QUERY_CONCURRENT=%d needs %d bytes of game-cluster memory "+
				"(%d leaders plus %d parallel workers plus %d build sessions at %d each, plus %d reserved), "+
				"but GAME_DB_MEMORY_BYTES is %d: raise GAME_DB_MEMORY_BYTES or lower QUERY_CONCURRENT",
			cfg.Concurrent, need, cfg.Concurrent, MaxParallelWorkers, MaxBuildSessions, cfg.ProcessMemoryBytes,
			ReservedMemoryBytes, cfg.GameDBMemoryBytes)
	}
	return cfg, nil
}
