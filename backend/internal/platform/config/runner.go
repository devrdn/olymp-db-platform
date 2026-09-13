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
	// published outside the private network.
	ListenAddr string
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
	// ExtraFunctions are functions an operator has added to the allow-list
	// after a pilot, without waiting for a release (section 5, point 3).
	ExtraFunctions []string

	// GameDBMemoryBytes is the memory limit of the game cluster's container,
	// in bytes, when the deployment declares it (GAME_DB_MEMORY_BYTES). It is
	// not used to run anything — it exists so the runner can refuse to start
	// with a concurrency the cluster cannot hold. Zero means undeclared, which
	// a development cluster with no cgroup limit is, and then the check does
	// not run.
	GameDBMemoryBytes int64
}

// The game cluster's memory is the bound the whole admission-control story
// rests on, and these numbers turn QUERY_CONCURRENT into a demand on it.
//
// PerProcessMemoryBytes is the per-process cap the game cluster's backends run
// under (deploy ulimits.data). Under it, a backend that tries to allocate more
// gets a NULL from malloc and raises PostgreSQL's own "out of memory" ERROR —
// in that one backend, leaving the postmaster and every other session alone —
// rather than the container's cgroup limit being reached and the Linux OOM
// killer taking the backend, which the postmaster treats as a crash. Measured
// against the deployment's own image: legitimate work (a template build's COPY
// and index creation, CREATE DATABASE … TEMPLATE, a parallel query's dynamic
// shared memory, which is shared and does not count against the cap) fits
// comfortably, and a query that manufactures a gigabyte-scale value fails
// cleanly. It must equal the ulimit in deploy/docker-compose.yml.
//
// MaxParallelWorkersPerQuery is how many parallel worker processes one
// participant query may add to its leader, each with its own cap. It equals
// max_parallel_workers_per_gather on the participant roles (internal/gamedb),
// and a participant cannot raise it: SET is not a statement the SQL validator
// admits. So one query occupies at most (1 + this) processes at the cap.
//
// ReservedMemoryBytes is what the cluster needs before any participant query
// runs: shared_buffers, the postmaster and its background workers, and the
// headroom a provisioning build or autovacuum takes. A gibibyte and a half is
// above the sum measured for the pilot's settings.
//
// So a container holds QUERY_CONCURRENT × (1 + MaxParallelWorkersPerQuery) ×
// PerProcessMemoryBytes + ReservedMemoryBytes at worst, and GAME_DB_MEMORY
// must be at least that: at the default concurrency of eight, 8 × 2 × 256 MiB
// + 1.5 GiB = 5.5 GiB, which deploy/.env.example rounds up to a six-gibibyte
// GAME_DB_MEMORY. Lower one and the others can come down with it.
const (
	PerProcessMemoryBytes      = 256 << 20
	MaxParallelWorkersPerQuery = 1
	ReservedMemoryBytes        = 1536 << 20
)

// LoadRunner reads the Query Runner's configuration from the environment.
func LoadRunner() (Runner, error) {
	cfg := Runner{
		Env:        envOrDefault("ENV", "development"),
		LogLevel:   envOrDefault("LOG_LEVEL", "info"),
		ListenAddr: envOrDefault("QUERY_RUNNER_ADDR", ":9100"),
	}

	var err error
	if cfg.GameDBDSN, err = requiredEnv("GAME_DB_DSN"); err != nil {
		return Runner{}, err
	}
	cfg.GameDBWriterDSN = os.Getenv("GAME_DB_WRITER_DSN")
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

	if cfg.GameDBMemoryBytes, err = int64Env("GAME_DB_MEMORY_BYTES", 0); err != nil {
		return Runner{}, err
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
	// and the OOM killer restarts the cluster. Caught here when the container's
	// limit is declared, so it is a failed deploy and not a discovery during a
	// contest. Undeclared (a development cluster with neither a cgroup limit
	// nor the ulimit) leaves nothing to check against.
	if cfg.GameDBMemoryBytes > 0 {
		perQuery := int64(PerProcessMemoryBytes) * (1 + MaxParallelWorkersPerQuery)
		need := int64(cfg.Concurrent)*perQuery + ReservedMemoryBytes
		if need > cfg.GameDBMemoryBytes {
			return Runner{}, fmt.Errorf(
				"QUERY_CONCURRENT=%d needs %d bytes of game-cluster memory "+
					"(%d per query — %d per process × (1 + %d workers) — plus %d reserved), "+
					"but GAME_DB_MEMORY_BYTES is %d: raise GAME_DB_MEMORY or lower QUERY_CONCURRENT",
				cfg.Concurrent, need, perQuery, PerProcessMemoryBytes, MaxParallelWorkersPerQuery,
				ReservedMemoryBytes, cfg.GameDBMemoryBytes)
		}
	}
	return cfg, nil
}
