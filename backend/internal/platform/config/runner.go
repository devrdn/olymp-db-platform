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
// rests on, and these two numbers turn QUERY_CONCURRENT into a demand on it.
//
// perQueryMemoryBytes is what one query may need at its worst. The SQL policy
// bounds a single manufactured value to kilobytes, but it deliberately does
// not bound a generator-fed aggregate — string_agg or array_agg of the
// longest bounded value over the longest bounded series — and that reaches
// PostgreSQL's ~1 GiB ceiling on any one value (measured at ~0.95 GiB on the
// test cluster). Bounding the aggregate too would mean summing memory across
// the plan, which is a memory accountant the service deliberately does not
// build; the honest figure is therefore the ceiling itself.
//
// reservedMemoryBytes is what the cluster needs before any query runs:
// shared_buffers, the parallel-query shared memory (shm_size, 256 MiB), the
// postmaster and the per-backend base. One gibibyte is comfortably above the
// sum for the pilot's settings.
//
// So a container holds QUERY_CONCURRENT × perQueryMemoryBytes +
// reservedMemoryBytes at worst, and GAME_DB_MEMORY must be at least that. At
// the default concurrency of eight that is nine gibibytes, which is why
// deploy/.env.example sets GAME_DB_MEMORY accordingly and an operator who
// lowers one lowers the other. The numbers are worst-case on purpose: the
// point is that the OOM killer never fires, so a runaway query fails with
// "out of memory" for its author rather than crashing every participant's
// session.
const (
	perQueryMemoryBytes = 1 << 30
	reservedMemoryBytes = 1 << 30
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
	// A concurrency the game cluster's memory cannot hold is a cluster that
	// flaps under load rather than degrades: the OOM killer takes one backend
	// and the postmaster restarts every session. Caught here when the limit
	// is declared, so it is a failed deploy and not a discovery during a
	// contest. Undeclared (a development cluster with no cgroup limit) leaves
	// nothing to check against.
	if cfg.GameDBMemoryBytes > 0 {
		need := int64(cfg.Concurrent)*perQueryMemoryBytes + reservedMemoryBytes
		if need > cfg.GameDBMemoryBytes {
			return Runner{}, fmt.Errorf(
				"QUERY_CONCURRENT=%d needs %d bytes of game-cluster memory (%d per query plus %d reserved), "+
					"but GAME_DB_MEMORY_BYTES is %d: raise GAME_DB_MEMORY or lower QUERY_CONCURRENT",
				cfg.Concurrent, need, perQueryMemoryBytes, reservedMemoryBytes, cfg.GameDBMemoryBytes)
		}
	}
	return cfg, nil
}
