package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Runner is the Query Runner service's configuration. It is separate from
// Config so the Core API never holds the game cluster's participant
// credentials. Figures stay primitives; the command assembles
// queryrunner.Limits from them (CLAUDE.md layout rule 7).
type Runner struct {
	Env      string
	LogLevel string
	// ListenAddr defaults to loopback and is never published outside the
	// private network.
	ListenAddr string
	// Token is the shared secret every call must carry: this service runs
	// whatever database and policy a request names. Required outside
	// development; never logged.
	Token string
	// GameDBDSN connects as game_reader; nothing else in the system holds it.
	GameDBDSN string
	// GameDBWriterDSN connects as game_writer, for contests whose policy
	// permits writing. Optional: without it a read-write contest is refused
	// rather than run as the reader.
	GameDBWriterDSN string
	ShutdownTimeout time.Duration

	// Deadline bounds one query. Unlike statement_timeout, which is USERSET
	// and can be turned off from inside a query, a context deadline holds.
	Deadline time.Duration
	MaxRows  int
	MaxBytes int
	// Concurrent is how many queries may run at once (two to three times the
	// cores), and QueueDepth how many may wait. A participant has at most one
	// query in flight, so Concurrent + QueueDepth at or above the roster size
	// queues rather than refuses a round where everybody presses Run.
	Concurrent int
	QueueDepth int
	// PerMinute bounds how often one participant may ask; the semaphore
	// alone lets cheap queries through one at a time.
	PerMinute int
	// IdleConnTimeout is how long a connection is kept for the database's
	// next query; zero keeps none. Kept connections count against
	// Concurrent, so this never raises how many backends the cluster holds.
	IdleConnTimeout time.Duration
	// ExtraFunctions extend the function allow-list without a release.
	ExtraFunctions []string

	// GameDBMemoryBytes is the game cluster container's memory limit, the
	// same variable and default compose sizes the container from. It is used
	// only to refuse a concurrency the cluster cannot hold; unset means the
	// default, and the check still runs.
	GameDBMemoryBytes int64
	// ProcessMemoryBytes is the per-process memory cap of the game cluster's
	// backends: the one value the sizing check, the compose ulimit and the
	// deploy-time self-check all read.
	ProcessMemoryBytes int64
}

// These turn QUERY_CONCURRENT into a demand on the game cluster's memory, the
// bound admission control rests on. At worst a container holds
//
//	(QUERY_CONCURRENT + MaxParallelWorkers + MaxBuildSessions) × ProcessMemoryBytes
//	+ ReservedMemoryBytes
//
// and GAME_DB_MEMORY_BYTES must be at least that. At the default concurrency
// of eight: (8 + 4 + 4) × 256 MiB + 2 GiB = 6 GiB, rounded up to 7 GiB in
// deploy/.env.example. Idle kept connections count within QUERY_CONCURRENT.
//
// MaxParallelWorkers is the cluster-wide parallel worker pool and must equal
// max_parallel_workers on the pg-game command. MaxBuildSessions equals the
// game_author role's CONNECTION LIMIT, since a build can run during a
// contest. AutovacuumWorkers must equal autovacuum_max_workers. Tests on the
// prepared cluster check all three. ReservedMemoryBytes covers
// shared_buffers, background processes, the 256 MiB /dev/shm segment,
// autovacuum and the provisioner's sessions, above the pilot's measured sum.
const (
	MaxParallelWorkers  = 4
	MaxBuildSessions    = 4
	AutovacuumWorkers   = 2
	ReservedMemoryBytes = 2 << 30

	DefaultIdleConnTimeout = 30 * time.Second
	MaxIdleConnTimeout     = 5 * time.Minute

	DefaultProcessMemoryBytes = 256 << 20

	// DefaultGameDBMemoryBytes matches the compose file's default container
	// limit; cmd/gamedb verifies the running container against it.
	DefaultGameDBMemoryBytes = 7 << 30
)

func LoadRunner() (Runner, error) {
	cfg := Runner{
		Env:      envOrDefault("ENV", "development"),
		LogLevel: envOrDefault("LOG_LEVEL", "info"),
		// Loopback by default, so a runner on a laptop is not reachable from
		// its network; compose names ":9100" on a private network.
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
	// Past the ceiling an idle backend only delays the reclaim sweep's DROP
	// DATABASE.
	if cfg.IdleConnTimeout < 0 || cfg.IdleConnTimeout > MaxIdleConnTimeout {
		return Runner{}, fmt.Errorf("QUERY_CONN_IDLE_TIMEOUT must be between 0 and %s, got %s",
			MaxIdleConnTimeout, cfg.IdleConnTimeout)
	}
	if cfg.PerMinute < 0 {
		return Runner{}, fmt.Errorf("QUERY_PER_MINUTE cannot be negative, got %d", cfg.PerMinute)
	}
	// Empty entries from stray commas are skipped, never allowed as "".
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

	// A zero here, usually a mistyped variable, would mean no limit at all.
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
	// The per-process cap turns over-allocation into one backend's "out of
	// memory" ERROR only while the container can hold every backend at the
	// cap; otherwise the OOM killer restarts the cluster. So a concurrency
	// that does not fit fails the deploy.
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
