package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func setRunnerRequired(t *testing.T) {
	t.Helper()
	t.Setenv("GAME_DB_DSN", "postgres://game_reader:secret@pg-game:5432/postgres")
}

func TestLoadRunnerAppliesTheArchitecturesFigures(t *testing.T) {
	setRunnerRequired(t)

	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Deadline != 5*time.Second {
		t.Fatalf("deadline = %s, want 5s", cfg.Deadline)
	}
	if cfg.MaxRows != 1000 || cfg.MaxBytes != 5<<20 {
		t.Fatalf("result limits = %d rows / %d bytes", cfg.MaxRows, cfg.MaxBytes)
	}
	// Loopback unless told otherwise: a runner started on a laptop must not be
	// reachable from the lecture hall's network. The compose file names ":9100"
	// explicitly, inside a network nothing outside can route into.
	if cfg.ListenAddr != "127.0.0.1:9100" {
		t.Fatalf("listen address = %q, want 127.0.0.1:9100", cfg.ListenAddr)
	}
	// Running plus waiting covers forty participants, one query each: a
	// round in which all of them press Run is queued, not refused.
	if cfg.Concurrent+cfg.QueueDepth < 40 {
		t.Fatalf("admission = %d running + %d waiting, fewer than the forty participants the olympiad expects",
			cfg.Concurrent, cfg.QueueDepth)
	}
}

func TestTheGameDatabaseIsRequired(t *testing.T) {
	// Without it there is nothing to run queries against, and a runner that
	// starts anyway would fail one participant at a time instead of failing
	// the deploy.
	t.Setenv("GAME_DB_DSN", "")

	if _, err := LoadRunner(); err == nil {
		t.Fatal("a runner with no game cluster started")
	}
}

// Each of these turns a limit into no limit at all, and a mistyped variable is
// exactly how that happens: `QUERY_MAX_ROWS=` reads as zero, and zero rows
// would mean every answer is empty while zero concurrency would mean none run.
// Refusing at startup is the difference between a misconfiguration and an
// olympiad that quietly has no bounds.
func TestALimitOfZeroIsRefusedRatherThanTakenLiterally(t *testing.T) {
	for _, name := range []string{"QUERY_MAX_ROWS", "QUERY_MAX_BYTES", "QUERY_CONCURRENT"} {
		t.Run(name, func(t *testing.T) {
			setRunnerRequired(t)
			t.Setenv(name, "0")

			if _, err := LoadRunner(); err == nil {
				t.Fatalf("%s=0 was accepted", name)
			}
		})
	}

	t.Run("QUERY_DEADLINE", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_DEADLINE", "0s")

		if _, err := LoadRunner(); err == nil {
			t.Fatal("a deadline of zero was accepted; it is the one bound SQL cannot lift")
		}
	})
}

// A queue of nothing is a real choice — refuse rather than wait — so zero is
// allowed here where it is refused above.
func TestAnEmptyQueueIsAllowedButANegativeOneIsNot(t *testing.T) {
	setRunnerRequired(t)
	t.Setenv("QUERY_QUEUE_DEPTH", "0")
	if _, err := LoadRunner(); err != nil {
		t.Fatalf("a queueless runner was refused: %v", err)
	}

	t.Setenv("QUERY_QUEUE_DEPTH", "-1")
	if _, err := LoadRunner(); err == nil {
		t.Fatal("a negative queue was accepted")
	}
}

// The game cluster runs in a container whose backends have a per-process
// memory cap (deploy ulimits.data): a query that over-allocates fails with an
// "out of memory" ERROR in its own backend instead of tripping the container's
// cgroup limit and the OOM killer, which would restart every session. That
// only holds while the container is large enough to hold every backend at the
// cap at once — QUERY_CONCURRENT queries, each a leader plus its parallel
// workers. When the container's limit is declared (GAME_DB_MEMORY_BYTES), the
// runner refuses to start with a concurrency it cannot hold, so the
// misconfiguration is a failed deploy rather than a cluster that flaps.
func TestConcurrencyMustFitTheDeclaredGameClusterMemory(t *testing.T) {
	// The arithmetic the check enforces, stated here so the test fails if the
	// constants drift from what the deployment is sized for.
	const cap = int64(DefaultProcessMemoryBytes)
	perBudget := func(concurrent int) int64 {
		return (int64(concurrent)+int64(MaxParallelWorkers)+int64(MaxBuildSessions))*cap + ReservedMemoryBytes
	}

	t.Run("a limit that cannot hold the concurrency is refused", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		// Just below what eight queries plus the reserve need.
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(perBudget(8)-1, 10))

		if _, err := LoadRunner(); err == nil {
			t.Fatal("a runner started with a concurrency its game cluster cannot hold")
		}
	})

	t.Run("the limit the deployment ships is accepted", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		// Seven gibibytes, the deploy default, holds eight.
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(7<<30, 10))

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("the shipped seven-gibibyte limit was refused at concurrency eight: %v", err)
		}
	})

	t.Run("exactly enough is accepted", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(perBudget(8), 10))

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("a limit exactly at the requirement was refused: %v", err)
		}
	})

	t.Run("lowering the concurrency lets a smaller limit through", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "1")
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(perBudget(1), 10))

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("one query at exactly its requirement was refused: %v", err)
		}
	})

	t.Run("an absent or empty limit falls back to the default and is still checked", func(t *testing.T) {
		// The check is never skipped: GAME_DB_MEMORY_BYTES is also what the
		// game cluster's container limit is interpolated from, with the same
		// default, so an unset variable means the default-sized container —
		// and a concurrency it cannot hold must still be refused.
		for _, value := range []string{"", "unset"} {
			setRunnerRequired(t)
			t.Setenv("QUERY_CONCURRENT", "64")
			if value == "unset" {
				t.Setenv("GAME_DB_MEMORY_BYTES", "")
				os.Unsetenv("GAME_DB_MEMORY_BYTES")
			} else {
				t.Setenv("GAME_DB_MEMORY_BYTES", value)
			}
			if _, err := LoadRunner(); err == nil {
				t.Fatalf("GAME_DB_MEMORY_BYTES %s: concurrency 64 started against the default-sized cluster", value)
			}
		}

		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		t.Setenv("GAME_DB_MEMORY_BYTES", "")
		cfg, err := LoadRunner()
		if err != nil {
			t.Fatalf("the default concurrency was refused against the default limit: %v", err)
		}
		if cfg.GameDBMemoryBytes != DefaultGameDBMemoryBytes {
			t.Fatalf("limit = %d, want the %d default", cfg.GameDBMemoryBytes, DefaultGameDBMemoryBytes)
		}
	})

	t.Run("a zero limit is refused rather than read as no limit", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("GAME_DB_MEMORY_BYTES", "0")
		if _, err := LoadRunner(); err == nil {
			t.Fatal("GAME_DB_MEMORY_BYTES=0 was accepted")
		}
	})
}

// The per-process cap the sizing check multiplies comes from the environment —
// the same GAME_DB_PROCESS_MEMORY_BYTES the compose ulimit and the deploy
// self-check read — not a constant, so the three cannot drift. A larger cap
// makes the same concurrency need more memory.
func TestTheProcessCapIsReadFromTheEnvironment(t *testing.T) {
	t.Run("default when unset", func(t *testing.T) {
		setRunnerRequired(t)
		cfg, err := LoadRunner()
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ProcessMemoryBytes != DefaultProcessMemoryBytes {
			t.Fatalf("cap = %d, want the %d default", cfg.ProcessMemoryBytes, DefaultProcessMemoryBytes)
		}
	})

	t.Run("a larger cap raises the memory the concurrency needs", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		// A 512 MiB cap doubles the per-process demand, so the seven gibibytes
		// that held eight at 256 MiB no longer does.
		t.Setenv("GAME_DB_PROCESS_MEMORY_BYTES", strconv.FormatInt(512<<20, 10))
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(7<<30, 10))
		if _, err := LoadRunner(); err == nil {
			t.Fatal("a 512 MiB cap at concurrency eight fit in seven gibibytes")
		}
	})

	t.Run("zero is refused", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("GAME_DB_PROCESS_MEMORY_BYTES", "0")
		if _, err := LoadRunner(); err == nil {
			t.Fatal("a zero per-process cap was accepted")
		}
	})
}

func TestTheAllowListCanBeExtendedFromTheEnvironment(t *testing.T) {
	setRunnerRequired(t)
	// Spacing and a trailing comma are what a person actually types; a stray
	// empty entry would allow a function named "".
	t.Setenv("QUERY_EXTRA_FUNCTIONS", " soundex , levenshtein ,")

	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ExtraFunctions) != 2 ||
		cfg.ExtraFunctions[0] != "soundex" || cfg.ExtraFunctions[1] != "levenshtein" {
		t.Fatalf("extra functions = %#v", cfg.ExtraFunctions)
	}
}

// The Core API may provision databases — the provisioner is one of its
// modules — but it must never hold a *participant's* credentials. The Query
// Runner is the only process that connects as game_reader or game_writer, and
// that is one of the two reasons it is a separate service.
func TestTheCoreConfigurationNeverReadsTheParticipantsCredentials(t *testing.T) {
	setRequired(t)
	t.Setenv("GAME_DB_DSN", "postgres://game_reader:secret@pg-game:5432/postgres")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	const participant = "postgres://game_reader:secret@pg-game:5432/postgres"
	if cfg.CoreDBDSN == participant || cfg.GameProvisionerDSN == participant {
		t.Fatal("the core configuration picked up the participant role's DSN")
	}
	// And its own provisioning credentials are a separate variable, so the two
	// cannot be set to the same thing by a deployment that shortens a step.
	t.Setenv("GAME_PROVISIONER_DSN", "postgres://provisioner:other@pg-game:5432/postgres")
	// A provisioner now also has to name what the game-script role
	// authenticates with; that is a third credential again, and the subject
	// of this test is only that none of them is a participant's.
	t.Setenv("GAME_AUTHOR_PASSWORD", "an-author-password")
	again, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if again.GameProvisionerDSN == participant {
		t.Fatal("the provisioner is configured as a participant")
	}
}

// The writer's credentials are optional, and their absence has to be a value
// the runner can act on: a read-write contest is then refused rather than run
// as the reader.
func TestRunnerReadsTheOptionalWriterDSN(t *testing.T) {
	t.Setenv("GAME_DB_DSN", "postgres://game_reader:secret@pg-game:5432/postgres")
	t.Setenv("GAME_DB_WRITER_DSN", "")

	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("LoadRunner() returned error: %v", err)
	}
	if cfg.GameDBWriterDSN != "" {
		t.Errorf("GameDBWriterDSN = %q, want empty when unset", cfg.GameDBWriterDSN)
	}

	t.Setenv("GAME_DB_WRITER_DSN", "postgres://game_writer:secret@pg-game:5432/postgres")
	cfg, err = LoadRunner()
	if err != nil {
		t.Fatalf("LoadRunner() returned error: %v", err)
	}
	if cfg.GameDBWriterDSN == "" {
		t.Error("GameDBWriterDSN was not read from the environment")
	}
}

// The runner obeys the database and policy a request names, so outside
// development it refuses to start without the token that proves a request came
// from the Core API.
func TestTheRunnerRequiresItsTokenOutsideDevelopment(t *testing.T) {
	t.Setenv("QUERY_RUNNER_TOKEN", "") // not inherited from the shell
	setRunnerRequired(t)
	t.Setenv("ENV", "production")

	if _, err := LoadRunner(); err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
		t.Fatalf("LoadRunner() without QUERY_RUNNER_TOKEN in production = %v, want an error naming it", err)
	}

	short := strings.Repeat("t", 31)
	t.Setenv("QUERY_RUNNER_TOKEN", short)
	_, err := LoadRunner()
	if err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
		t.Fatalf("LoadRunner() with a 31-byte token = %v, want an error naming it", err)
	}
	if strings.Contains(err.Error(), short) {
		t.Fatalf("the refusal repeats the token: %q", err.Error())
	}

	token := strings.Repeat("t", 32)
	t.Setenv("QUERY_RUNNER_TOKEN", token)
	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("LoadRunner() with a 32-byte token: %v", err)
	}
	if cfg.Token != token {
		t.Error("Token is not the configured value")
	}
}

// Any value other than "development" is treated as production, the same rule
// the API applies to its own secrets: a typo in ENV must not switch a check off.
func TestAnUnrecognisedEnvironmentIsHeldToProductionsRule(t *testing.T) {
	t.Setenv("QUERY_RUNNER_TOKEN", "") // not inherited from the shell
	setRunnerRequired(t)
	t.Setenv("ENV", "staging")

	if _, err := LoadRunner(); err == nil || !strings.Contains(err.Error(), "QUERY_RUNNER_TOKEN") {
		t.Fatalf("LoadRunner() with ENV=staging and no token = %v, want an error naming it", err)
	}
}

func TestADevelopmentRunnerMayOmitItsToken(t *testing.T) {
	t.Setenv("QUERY_RUNNER_TOKEN", "") // not inherited from the shell
	setRunnerRequired(t)
	t.Setenv("ENV", "development")

	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("LoadRunner() in development without a token: %v", err)
	}
	if cfg.Token != "" {
		t.Errorf("Token = %q, want empty", cfg.Token)
	}
}

// The runner's own credentials and its token, held to the same rule as the
// API's: a "change-me" value from deploy/.env.example is refused outside
// development, naming the variable and never the value.
func TestARunnerPlaceholderCredentialIsRefusedOutsideDevelopment(t *testing.T) {
	for name, value := range map[string]string{
		"QUERY_RUNNER_TOKEN": "change-me-to-the-output-of-openssl-rand-hex",
		"GAME_DB_DSN":        "postgres://game_reader:change-me-before-first-run@pg-game:5432/postgres",
		"GAME_DB_WRITER_DSN": "postgres://game_writer:CHANGE-ME-before-first-run@pg-game:5432/postgres",
	} {
		t.Run(name, func(t *testing.T) {
			setRunnerRequired(t)
			t.Setenv("ENV", "production")
			t.Setenv("QUERY_RUNNER_TOKEN", strings.Repeat("t", 32))
			t.Setenv("GAME_DB_WRITER_DSN", "")
			if _, err := LoadRunner(); err != nil {
				t.Fatalf("the baseline production configuration was refused: %v", err)
			}

			t.Setenv(name, value)
			_, err := LoadRunner()
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("LoadRunner() with a placeholder %s = %v, want an error naming it", name, err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "change-me") {
				t.Fatalf("the refusal repeats the value: %q", err.Error())
			}

			t.Setenv("ENV", "development")
			if _, err := LoadRunner(); err != nil {
				t.Fatalf("development refused a placeholder %s: %v", name, err)
			}
		})
	}
}
