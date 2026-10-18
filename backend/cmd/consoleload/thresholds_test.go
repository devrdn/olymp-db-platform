package main

import (
	"strings"
	"testing"
	"time"
)

var testBar = thresholds{P95: 2 * time.Second, MaxRefused: 0.01, MaxErrors: 0.005}

// healthyRun is a run that clears testBar with room to spare.
func healthyRun(name string) runSummary {
	return runSummary{Name: name, Queries: 1000, All: latency{Count: 1000, P95: 900}}
}

func TestJudge(t *testing.T) {
	tests := []struct {
		name   string
		change func(*runSummary)
		// failing is the check that should fail, empty when all pass.
		failing string
	}{
		{name: "a healthy run passes", change: func(*runSummary) {}},
		{name: "a p95 at the limit passes", change: func(r *runSummary) { r.All.P95 = 2000 }},
		{name: "a p95 over the limit fails", change: func(r *runSummary) { r.All.P95 = 2001 }, failing: checkP95},
		{name: "refusals at the limit pass", change: func(r *runSummary) { r.Refused = 10 }},
		{name: "refusals over the limit fail", change: func(r *runSummary) { r.Refused = 11 }, failing: checkRefused},
		{name: "errors at the limit pass", change: func(r *runSummary) { r.Errors = 5 }},
		{name: "errors over the limit fail", change: func(r *runSummary) { r.Errors = 6 }, failing: checkErrors},
		{name: "one server error fails", change: func(r *runSummary) { r.ServerErrors = 1 }, failing: checkServerErrors},
		{name: "a run with no queries fails", change: func(r *runSummary) { *r = runSummary{Name: r.Name} }, failing: checkQueries},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := healthyRun("steady-40")
			tt.change(&run)

			v := testBar.judge([]runSummary{run})

			if v.Passed != (tt.failing == "") {
				t.Fatalf("passed = %v, want %v: %+v", v.Passed, tt.failing == "", v.Checks)
			}
			for _, c := range v.Checks {
				if c.Run != "steady-40" {
					t.Errorf("check %q names run %q", c.Name, c.Run)
				}
				if want := c.Name != tt.failing; c.Passed != want {
					t.Errorf("check %q passed = %v, want %v (measured %s, limit %s)",
						c.Name, c.Passed, want, c.Measured, c.Limit)
				}
			}
		})
	}
}

func TestJudgeFailsWhenAnyRunFails(t *testing.T) {
	slow := healthyRun("steady-60")
	slow.All.P95 = 5000

	v := testBar.judge([]runSummary{healthyRun("steady-40"), slow})

	if v.Passed {
		t.Fatalf("a slow second run passed: %+v", v.Checks)
	}
	for _, c := range v.Checks {
		if !c.Passed && (c.Run != "steady-60" || c.Name != checkP95) {
			t.Errorf("unexpected failure: %+v", c)
		}
	}
}

func TestJudgeFailsWithNoRuns(t *testing.T) {
	if v := testBar.judge(nil); v.Passed {
		t.Fatalf("nothing measured, and it passed: %+v", v)
	}
}

func TestVerdictMarkdownNamesEveryFailure(t *testing.T) {
	run := healthyRun("steady-40")
	run.All.P95, run.ServerErrors = 3100, 2

	text := testBar.judge([]runSummary{run}).markdown()

	for _, want := range []string{"FAILED", "| steady-40 | " + checkP95 + " | 3100 ms | at most 2000 ms | fail |",
		"| steady-40 | " + checkServerErrors + " | 2 | none | fail |"} {
		if !strings.Contains(text, want) {
			t.Errorf("the verdict lacks %q:\n%s", want, text)
		}
	}
}
