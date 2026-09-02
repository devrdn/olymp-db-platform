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
	Concurrent int
	QueueDepth int
	// PerMinute bounds how often one participant may ask. The semaphore
	// cannot: a thousand cheap queries pass it one at a time.
	PerMinute int
	// ExtraFunctions are functions an operator has added to the allow-list
	// after a pilot, without waiting for a release (section 5, point 3).
	ExtraFunctions []string
}

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
	if cfg.QueueDepth, err = intEnv("QUERY_QUEUE_DEPTH", 16); err != nil {
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
	return cfg, nil
}
