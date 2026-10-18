package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

func TestTheBetaProfileSetsTheRunAndItsThresholds(t *testing.T) {
	cfg, err := parseFlags([]string{"-source-template", "game_tpl_x", "-profile", "beta"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}

	if !slices.Equal(cfg.participants, []int{40}) || !slices.Equal(cfg.shapes, []string{shapeSteady}) {
		t.Errorf("participants %v, shapes %v; want [40] and [steady]", cfg.participants, cfg.shapes)
	}
	if cfg.steadyFor != 15*time.Minute || cfg.thinkMin != 5*time.Second || cfg.thinkMax != 30*time.Second {
		t.Errorf("steady %s, think %s to %s; want 15m, 5s to 30s", cfg.steadyFor, cfg.thinkMin, cfg.thinkMax)
	}
	if want := (mix{KindCheap: 40, KindMid: 30, KindExpensive: 20, KindWrite: 10}); !maps.Equal(cfg.mix, want) {
		t.Errorf("mix %v, want %v", cfg.mix, want)
	}
	if cfg.mode != contests.ModeReadWrite {
		t.Errorf("mode %q, want read_write", cfg.mode)
	}
	if cfg.profile != "beta" || cfg.thresholds == nil || *cfg.thresholds != profiles["beta"].thresholds {
		t.Errorf("profile %q, thresholds %+v; want beta's", cfg.profile, cfg.thresholds)
	}
}

func TestAFlagGivenOnTheCommandLineOverridesTheProfile(t *testing.T) {
	cfg, err := parseFlags([]string{"-source-template", "game_tpl_x", "-participants", "60", "-profile", "beta",
		"-steady-duration", "5m"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !slices.Equal(cfg.participants, []int{60}) || cfg.steadyFor != 5*time.Minute {
		t.Errorf("participants %v, steady %s; want [60] and 5m", cfg.participants, cfg.steadyFor)
	}
	if cfg.thinkMin != 5*time.Second || cfg.thresholds == nil {
		t.Errorf("the rest of the profile was lost: think-min %s, thresholds %v", cfg.thinkMin, cfg.thresholds)
	}
}

func TestWithoutAProfileARunIsOnlyMeasured(t *testing.T) {
	cfg, err := parseFlags([]string{"-source-template", "game_tpl_x"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.thresholds != nil {
		t.Errorf("thresholds %+v without a profile", cfg.thresholds)
	}
}

func TestAnUnknownProfileIsRefused(t *testing.T) {
	_, err := parseFlags([]string{"-source-template", "game_tpl_x", "-profile", "gamma"})
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("parseFlags = %v, want a refusal naming the known profiles", err)
	}
}

func TestTheLongestProfileRunIsTheBetaProfilesWholeRun(t *testing.T) {
	beta, err := parseFlags([]string{"-source-template", "game_tpl_x", "-profile", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := longestProfileRun(); err != nil || got != beta.horizon() {
		t.Fatalf("longestProfileRun = %s, %v; want the beta profile's %s", got, err, beta.horizon())
	}
}

func TestTheLongestProfileRunIsAnErrorWhenAProfileCannotBeRead(t *testing.T) {
	t.Setenv("GAME_PROVISION_WORKERS", "0")
	if got, err := longestProfileRun(); err == nil {
		t.Fatalf("longestProfileRun = %s with an unreadable environment, want an error", got)
	}
}
