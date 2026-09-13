package config

import (
	"strconv"
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
	if cfg.ListenAddr != ":9100" {
		t.Fatalf("listen address = %q", cfg.ListenAddr)
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

// The game cluster runs in a container with a memory limit, and one query can,
// worst case, make a backend build close to PostgreSQL's ~1 GiB per-value
// ceiling — a string_agg or array_agg of the largest bounded value the
// validator admits, over the longest series it admits. QUERY_CONCURRENT of
// those at once must fit the container, or the Linux OOM killer takes a
// backend and the postmaster restarts every session. When the container's
// limit is declared (GAME_DB_MEMORY_BYTES), the runner refuses to start with
// a concurrency the limit cannot hold, so the misconfiguration is a failed
// deploy rather than a cluster that flaps under load.
func TestConcurrencyMustFitTheDeclaredGameClusterMemory(t *testing.T) {
	t.Run("a limit that cannot hold the concurrency is refused", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		// Two gibibytes cannot hold eight worst-case queries plus the cluster's
		// own shared memory — this is the size B1 found flapping.
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(2<<30, 10))

		if _, err := LoadRunner(); err == nil {
			t.Fatal("a runner started with a concurrency its game cluster cannot hold")
		}
	})

	t.Run("a limit with room is accepted", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "8")
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(10<<30, 10))

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("a runner with ample game-cluster memory was refused: %v", err)
		}
	})

	t.Run("lowering the concurrency lets a smaller limit through", func(t *testing.T) {
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "1")
		t.Setenv("GAME_DB_MEMORY_BYTES", strconv.FormatInt(3<<30, 10))

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("one query in three gibibytes was refused: %v", err)
		}
	})

	t.Run("without the limit declared the check does not run", func(t *testing.T) {
		// The limit is optional: a development runner against a cluster with no
		// cgroup limit has nothing to check against, and must still start.
		setRunnerRequired(t)
		t.Setenv("QUERY_CONCURRENT", "64")
		t.Setenv("GAME_DB_MEMORY_BYTES", "")

		if _, err := LoadRunner(); err != nil {
			t.Fatalf("a runner with no declared game-cluster memory was refused: %v", err)
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
