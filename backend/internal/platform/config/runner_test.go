package config

import (
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

// The Core API's configuration must not be able to name the game cluster: the
// separation of credentials is one of the two reasons the runner is its own
// service, and one struct with both fields would put them in the same process.
func TestTheCoreConfigurationHasNoGameCluster(t *testing.T) {
	setRequired(t)
	t.Setenv("GAME_DB_DSN", "postgres://game_reader:secret@pg-game:5432/postgres")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CoreDBDSN == "postgres://game_reader:secret@pg-game:5432/postgres" {
		t.Fatal("the core configuration read the game cluster's DSN")
	}
}
