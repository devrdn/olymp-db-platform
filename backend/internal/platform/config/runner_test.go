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
	if cfg.ListenAddr != "127.0.0.1:9100" {
		t.Fatalf("listen address = %q, want 127.0.0.1:9100", cfg.ListenAddr)
	}
	if cfg.Concurrent+cfg.QueueDepth < 40 {
		t.Fatalf("admission = %d running + %d waiting, fewer than the forty participants the olympiad expects",
			cfg.Concurrent, cfg.QueueDepth)
	}
}

func TestTheGameDatabaseIsRequired(t *testing.T) {
	t.Setenv("GAME_DB_DSN", "")

	if _, err := LoadRunner(); err == nil {
		t.Fatal("a runner with no game cluster started")
	}
}

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

func TestTheIdleConnectionTimeoutHasADefaultAndBounds(t *testing.T) {
	setRunnerRequired(t)
	cfg, err := LoadRunner()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.IdleConnTimeout != 30*time.Second {
		t.Fatalf("idle connection timeout = %s, want 30s by default", cfg.IdleConnTimeout)
	}

	for value, accepted := range map[string]bool{
		"0s":  true,
		"10s": true,
		"5m":  true,
		"-1s": false,
		"6m":  false,
		"ten": false,
	} {
		t.Run(value, func(t *testing.T) {
			setRunnerRequired(t)
			t.Setenv("QUERY_CONN_IDLE_TIMEOUT", value)
			cfg, err := LoadRunner()
			if accepted && err != nil {
				t.Fatalf("QUERY_CONN_IDLE_TIMEOUT=%s was refused: %v", value, err)
			}
			if !accepted && err == nil {
				t.Fatalf("QUERY_CONN_IDLE_TIMEOUT=%s was accepted as %s", value, cfg.IdleConnTimeout)
			}
		})
	}
}

func TestConcurrencyMustFitTheDeclaredGameClusterMemory(t *testing.T) {
	// Stated here so the test fails if the constants drift.
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
		// Unset means the default-sized container, still checked.
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
	t.Setenv("GAME_PROVISIONER_DSN", "postgres://provisioner:other@pg-game:5432/postgres")
	t.Setenv("GAME_AUTHOR_PASSWORD", "an-author-password")
	again, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if again.GameProvisionerDSN == participant {
		t.Fatal("the provisioner is configured as a participant")
	}
}

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
