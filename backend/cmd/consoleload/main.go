// Command consoleload drives the participant's SQL console with synthetic
// participants and reports what they experienced: latency over the whole HTTP
// round trip, refusals by status and code, CPU and memory of watched processes
// and containers, concurrent queries on the game cluster, and provisioning
// time and disk.
//
// It uses the participant's real path (CLAUDE.md rule 10): POST
// /api/v1/contests/{id}/query through the API and the Query Runner, with no
// limit bypassed. Two load shapes: steady (query, think 10-30 s, repeat) and
// burst (everybody presses Run within one second, to test the runner's
// queue).
//
// It measures whatever listens at -api and never configures it. Participants
// are ordinary accounts with random passwords kept only in memory, signing in
// through /auth/login. It writes into the installation's own databases and
// removes everything it created at the end, except the sign-in limiter's
// counter, which expires on its own. `consoleload sweep` cleans up a killed
// run by login prefix.
//
// Usage:
//
//	consoleload -source-template game_tpl_x [flags]   set up, provision, load, tear down
//	consoleload -keep ...                              leave the fixture for another run
//	consoleload -fixture DIR/fixture.json [flags]      reuse a kept fixture, then tear it down
//	consoleload -fixture ... -copy-setting name=value  try a cluster setting on the copies only
//	consoleload sweep                                  remove whatever an interrupted run left
//
// It reads CORE_DB_DSN and GAME_PROVISIONER_DSN (the provisioning role on the
// game cluster) — the same two the API itself is given. `make loadtest` passes
// both.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "consoleload: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	api            string
	sourceTemplate string
	participants   []int
	shapes         []string

	steadyFor time.Duration
	thinkMin  time.Duration
	thinkMax  time.Duration

	burstRounds int
	burstGap    time.Duration
	burstWindow time.Duration

	cooldown time.Duration
	mix      mix
	seed     int64

	headroom     int
	workers      int
	copyStrategy string

	watchPIDs       map[string]int
	watchContainers map[string]string

	// copySettings apply to the fixture's own copies only.
	copySettings map[string]string

	out     string
	label   string
	keep    bool
	fixture string
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "sweep" {
		return runSweep(args[1:])
	}

	cfg, err := parseFlags(args)
	if err != nil {
		return err
	}

	// Ctrl+C stops the load at once; teardown gets its own context below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := openStores(ctx, cfg.copyStrategy)
	if err != nil {
		return err
	}
	defer st.close()

	// Every file goes through this root, so none lands outside it.
	if err := os.MkdirAll(cfg.out, 0o750); err != nil {
		return fmt.Errorf("create the output directory: %w", err)
	}
	out, err := os.OpenRoot(cfg.out)
	if err != nil {
		return fmt.Errorf("open the output directory: %w", err)
	}
	defer out.Close()
	fmt.Printf("results go to %s\n", cfg.out)

	return execute(ctx, st, cfg, out)
}

func parseFlags(args []string) (config, error) {
	flags := flag.NewFlagSet("consoleload", flag.ContinueOnError)
	cfg := config{}

	flags.StringVar(&cfg.api, "api", envOr("LOADTEST_API", "http://localhost:8080"),
		"base URL of the API, without /api/v1")
	flags.StringVar(&cfg.sourceTemplate, "source-template", "",
		"a built game template on the game cluster to copy the test game from (not modified)")
	participants := flags.String("participants", "30,40", "participant counts to run, comma-separated")
	shapes := flags.String("shapes", "steady,burst", "load shapes to run at each count: steady, burst")

	flags.DurationVar(&cfg.steadyFor, "steady-duration", 5*time.Minute, "how long a steady run lasts")
	flags.DurationVar(&cfg.thinkMin, "think-min", 10*time.Second, "shortest pause between one participant's queries")
	flags.DurationVar(&cfg.thinkMax, "think-max", 30*time.Second, "longest pause between one participant's queries")

	flags.IntVar(&cfg.burstRounds, "burst-rounds", 5, "how many bursts a burst run fires")
	flags.DurationVar(&cfg.burstGap, "burst-gap", 30*time.Second, "pause between the end of one burst and the next")
	flags.DurationVar(&cfg.burstWindow, "burst-window", time.Second, "every participant presses Run within this window")

	flags.DurationVar(&cfg.cooldown, "cooldown", 30*time.Second, "pause between runs")
	mixFlag := flags.String("mix", "50,30,20", "percentages of cheap, mid and expensive queries")
	flags.Int64Var(&cfg.seed, "seed", 1, "random seed, so two runs send the same sequence of queries")

	// The pool tender's own defaults and variables.
	flags.IntVar(&cfg.headroom, "headroom", envInt("GAME_POOL_DEPTH", 10),
		"spare copies beyond the roster, as GAME_POOL_DEPTH")
	flags.IntVar(&cfg.workers, "workers", envInt("GAME_PROVISION_WORKERS", 3),
		"copies made at once, as GAME_PROVISION_WORKERS")
	flags.StringVar(&cfg.copyStrategy, "copy-strategy", os.Getenv("GAME_COPY_STRATEGY"),
		"WAL_LOG or FILE_COPY, as GAME_COPY_STRATEGY; empty leaves PostgreSQL's default")

	copySettings := flags.String("copy-setting", "",
		"run-time parameters for the test's own copies only, name=value,... (DEFAULT removes one), "+
			"e.g. max_parallel_workers_per_gather=0")
	pids := flags.String("watch-pid", "", "processes to sample, name=pid,... (e.g. api=123,runner=456)")
	containers := flags.String("watch-container", "",
		"containers to sample, name=container,... (e.g. game=db-contest-pg-game-1)")

	flags.StringVar(&cfg.out, "out", filepath.Join(os.TempDir(), "dbcontest-consoleload-"+time.Now().Format("20060102-150405")),
		"directory for the results")
	flags.StringVar(&cfg.label, "label", "",
		"a line describing what is being measured, e.g. the Query Runner's limits; printed at the top of the report")
	flags.BoolVar(&cfg.keep, "keep", false, "leave the fixture in place and write fixture.json for -fixture")
	flags.StringVar(&cfg.fixture, "fixture", "", "reuse the fixture a -keep run left, instead of creating one")

	if err := flags.Parse(args); err != nil {
		return config{}, err
	}

	var err error
	if cfg.participants, err = intList(*participants); err != nil {
		return config{}, fmt.Errorf("-participants: %w", err)
	}
	for _, shape := range strings.Split(*shapes, ",") {
		shape = strings.TrimSpace(shape)
		if shape != shapeSteady && shape != shapeBurst {
			return config{}, fmt.Errorf("-shapes: unknown shape %q", shape)
		}
		cfg.shapes = append(cfg.shapes, shape)
	}
	if cfg.mix, err = parseMix(*mixFlag); err != nil {
		return config{}, fmt.Errorf("-mix: %w", err)
	}
	if cfg.watchPIDs, err = pidList(*pids); err != nil {
		return config{}, fmt.Errorf("-watch-pid: %w", err)
	}
	if cfg.watchContainers, err = nameList(*containers); err != nil {
		return config{}, fmt.Errorf("-watch-container: %w", err)
	}
	if cfg.copySettings, err = nameList(*copySettings); err != nil {
		return config{}, fmt.Errorf("-copy-setting: %w", err)
	}

	switch {
	case cfg.fixture == "" && cfg.sourceTemplate == "":
		return config{}, errors.New("-source-template is required unless -fixture reuses a kept fixture")
	case cfg.thinkMin <= 0 || cfg.thinkMax < cfg.thinkMin:
		return config{}, errors.New("-think-min must be positive and no larger than -think-max")
	case cfg.burstRounds < 1:
		return config{}, errors.New("-burst-rounds must be at least 1")
	case cfg.workers < 1:
		return config{}, errors.New("-workers must be at least 1")
	case cfg.headroom < 0:
		return config{}, errors.New("-headroom cannot be negative")
	}
	for _, n := range cfg.participants {
		// CLAUDE.md rule 2: each participant is an account and a database.
		if n < 1 || n > maxParticipants {
			return config{}, fmt.Errorf("-participants: %d is outside 1..%d", n, maxParticipants)
		}
	}
	return cfg, nil
}

// maxParticipants bounds one run, so a typo cannot ask for ten thousand
// copies.
const maxParticipants = 500

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return n
	}
	return fallback
}

func intList(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", part)
		}
		out = append(out, n)
	}
	return out, nil
}

func nameList(s string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" || value == "" {
			return nil, fmt.Errorf("%q is not name=value", part)
		}
		out[name] = value
	}
	return out, nil
}

func pidList(s string) (map[string]int, error) {
	names, err := nameList(s)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(names))
	for name, value := range names {
		pid, err := strconv.Atoi(value)
		if err != nil || pid < 1 {
			return nil, fmt.Errorf("%q is not a process id", value)
		}
		out[name] = pid
	}
	return out, nil
}
