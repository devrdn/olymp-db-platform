package main

import (
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
