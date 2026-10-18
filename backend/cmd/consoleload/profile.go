package main

import (
	"flag"
	"fmt"
	"strings"
	"time"
)

// profile is a named run: flag values applied as if given on the command
// line, unless they were, and the thresholds every run must clear.
type profile struct {
	flags      map[string]string
	thresholds thresholds
}

var profiles = map[string]profile{
	// beta asks whether the installation holds the first beta: about forty
	// remote participants in one read-write contest of the detective game.
	//
	// The run is steady, not a burst, because people read between queries.
	// A pause of 5 to 30 s (a query every 17 s on average) is busier than a
	// class: it includes the quick re-run after a typo and stays under
	// QUERY_PER_MINUTE's default of 30, so no refusal is the rate limit's.
	// Cheap lookups and joins dominate as they do for people finding their
	// way; a fifth are full-table aggregates, the slow mistake everybody
	// makes; a tenth are writes, since the contest is read-write. Fifteen
	// minutes is about 2000 queries, enough for a p95 resting on a hundred
	// of them and for memory and connections to settle.
	//
	// The thresholds are what a participant would notice:
	//   - p95 at most 2.5 s, half of QUERY_DEADLINE's default of 5 s, so the
	//     slowest queries could still double under a heavier hour and answer;
	//   - at most 1% refused: none of these queries breaks a rule, so a
	//     refusal is the server's doing, and 1% lets one stray through
	//     without letting a systematic one (a wrong template, a tripped limit);
	//   - at most 0.5% with no usable answer, a dropped connection or a
	//     garbled body, which the participant sees as a broken page;
	//   - no 5xx at all: a busy queue, a timeout or an internal error is a
	//     failure with forty people, never a matter of degree.
	"beta": {
		flags: map[string]string{
			"participants":    "40",
			"shapes":          shapeSteady,
			"steady-duration": "15m",
			"think-min":       "5s",
			"think-max":       "30s",
			"mix":             "40,30,20,10",
			"mode":            "read_write",
		},
		thresholds: thresholds{P95: 2500 * time.Millisecond, MaxRefused: 0.01, MaxErrors: 0.005},
	},
}

// applyProfile sets the named profile's flags that the command line left
// alone, so they pass the same parsing and checks as typed ones.
func applyProfile(flags *flag.FlagSet, name string) (*thresholds, error) {
	p, ok := profiles[name]
	if !ok {
		return nil, fmt.Errorf("-profile: unknown profile %q (known: %s)", name, strings.Join(sortedKeys(profiles), ", "))
	}
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, flagName := range sortedKeys(p.flags) {
		if given[flagName] {
			continue
		}
		if err := flags.Set(flagName, p.flags[flagName]); err != nil {
			return nil, fmt.Errorf("-profile %s: -%s: %w", name, flagName, err)
		}
	}
	return &p.thresholds, nil
}

// longestProfileRun is how long the longest profile's run can take, setup
// and teardown included; sweep leaves a younger run alone, as it may still be
// going.
func longestProfileRun() time.Duration {
	var longest time.Duration
	for _, name := range sortedKeys(profiles) {
		// The template is only required, never read, here.
		if cfg, err := parseFlags([]string{"-profile", name, "-source-template", "game_tpl_x"}); err == nil {
			longest = max(longest, cfg.horizon())
		}
	}
	return longest
}
