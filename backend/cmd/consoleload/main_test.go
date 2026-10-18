package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFlagsLetsOnlyAReadWriteContestWrite(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// refusal is part of the expected error, empty when parsing succeeds.
		refusal string
	}{
		{name: "reads in a read-only contest", args: []string{"-mix", "50,30,20"}},
		{name: "writes in a read-write contest", args: []string{"-mode", "read_write", "-mix", "40,30,20,10"}},
		{name: "reads in a read-write contest", args: []string{"-mode", "read_write"}},
		{name: "writes in a read-only contest", args: []string{"-mix", "40,30,20,10"}, refusal: "-mode read_write"},
		{name: "an unknown mode", args: []string{"-mode", "write_only"}, refusal: "-mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseFlags(append([]string{"-source-template", "game_tpl_x"}, tt.args...))
			if tt.refusal == "" {
				if err != nil {
					t.Fatalf("parseFlags: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.refusal) {
				t.Fatalf("parseFlags = %+v, %v; want an error naming %s", cfg, err, tt.refusal)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "passed", err: nil, want: 0},
		{name: "missed a threshold", err: errMissedThresholds, want: 2},
		{name: "missed a threshold and failed to tear down", err: errors.Join(errMissedThresholds, errors.New("teardown: drop")), want: 1},
		{name: "could not run", err: errors.New("CORE_DB_DSN and GAME_PROVISIONER_DSN must both be set"), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Fatalf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
