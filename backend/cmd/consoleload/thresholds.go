package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// thresholds is the bar every run of a profile is judged against. Each limit
// is inclusive, and a server error is never tolerated.
type thresholds struct {
	// P95 bounds the 95th percentile of every query's round trip, refusals
	// and errors included, since the participant waited for those too.
	P95 time.Duration `json:"p95_ns"`
	// MaxRefused and MaxErrors are shares of a run's queries, from 0 to 1.
	MaxRefused float64 `json:"max_refused"`
	MaxErrors  float64 `json:"max_errors"`
}

// The checks a run is judged by, as the verdict names them.
const (
	checkQueries      = "queries"
	checkP95          = "p95 latency"
	checkRefused      = "refused"
	checkErrors       = "errors"
	checkServerErrors = "server errors"
)

type check struct {
	Run      string `json:"run"`
	Name     string `json:"check"`
	Measured string `json:"measured"`
	Limit    string `json:"limit"`
	Passed   bool   `json:"passed"`
}

type verdict struct {
	Passed bool    `json:"passed"`
	Checks []check `json:"checks"`
}

// errMissedThresholds is a run that was measured and missed its profile's
// bar.
var errMissedThresholds = errors.New("the runs did not meet the profile's thresholds")

// judge passes only when there was at least one run and every check of every
// run passed.
func (t thresholds) judge(runs []runSummary) verdict {
	v := verdict{Passed: len(runs) > 0}
	add := func(c check) {
		v.Checks = append(v.Checks, c)
		v.Passed = v.Passed && c.Passed
	}
	for _, r := range runs {
		add(check{Run: r.Name, Name: checkQueries, Measured: fmt.Sprint(r.Queries), Limit: "at least 1",
			Passed: r.Queries > 0})
		if r.Queries == 0 {
			continue
		}
		add(check{Run: r.Name, Name: checkP95, Measured: fmt.Sprintf("%.0f ms", r.All.P95),
			Limit: fmt.Sprintf("at most %.0f ms", ms(t.P95)), Passed: r.All.P95 <= ms(t.P95)})
		add(t.share(r, checkRefused, r.Refused, t.MaxRefused))
		add(t.share(r, checkErrors, r.Errors, t.MaxErrors))
		add(check{Run: r.Name, Name: checkServerErrors, Measured: fmt.Sprint(r.ServerErrors), Limit: "none",
			Passed: r.ServerErrors == 0})
	}
	return v
}

func (thresholds) share(r runSummary, name string, n int, limit float64) check {
	got := float64(n) / float64(r.Queries)
	return check{
		Run: r.Name, Name: name,
		Measured: fmt.Sprintf("%.2f%% (%d of %d)", 100*got, n, r.Queries),
		Limit:    fmt.Sprintf("at most %.2f%%", 100*limit),
		Passed:   got <= limit,
	}
}

func (v verdict) markdown() string {
	var b strings.Builder
	if v.Passed {
		b.WriteString("**PASSED**: every run met the profile's thresholds.\n\n")
	} else {
		b.WriteString("**FAILED**: a run missed the profile's thresholds (marked fail below).\n\n")
	}
	b.WriteString("| run | check | measured | limit | result |\n|---|---|---:|---:|---|\n")
	for _, c := range v.Checks {
		result := "pass"
		if !c.Passed {
			result = "fail"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", c.Run, c.Name, c.Measured, c.Limit, result)
	}
	return b.String()
}
