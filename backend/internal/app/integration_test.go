package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// The alert rules and the dashboard are loaded by a Prometheus and a Grafana
// this repository never runs, so a metric renamed here would leave an alert
// that can never fire and nothing would say so. These tests hold both files to
// the series the API and the Query Runner actually register.

var dashboardRules = flag.String("dashboard-rules", "",
	"also write the dashboard's queries to this path as a rules file, for promtool to check their syntax")

const observabilityDir = "../../../deploy/observability/import"

var (
	rulesFile     = filepath.Join(observabilityDir, "dbcontest-rules.yml")
	rulesTestFile = filepath.Join(observabilityDir, "dbcontest-rules.test.yml")
	dashboardFile = filepath.Join(observabilityDir, "dbcontest-dashboard.json")
)

// scrapeLabels are attached by whatever scrapes the services, never by the
// services themselves.
var scrapeLabels = map[string]bool{"job": true, "instance": true}

// closedLabels take only values the Query Runner declares and exports at zero
// from the start, so an equality matcher on one can be held to the code.
// route, status and method take whatever the traffic brings.
var closedLabels = map[string]bool{"outcome": true, "reason": true, "limit": true}

// exportedSeries maps each series name the two services export to its labels
// and the values seen for each.
type exportedSeries map[string]map[string]map[string]bool

func registeredSeries(t *testing.T) exportedSeries {
	t.Helper()

	prom := metrics.NewPrometheus()
	// The HTTP vectors export nothing until a request is observed.
	prom.ObserveRequest("GET", "/api/v1/contests", 200, time.Millisecond, false)
	prom.ObserveRequest("GET", "/api/v1/contests/{contestID}/events", 200, time.Minute, true)
	metrics.NewGameReclaimCounters(prom)
	runner := metrics.NewQueryRunner(prom, queryrunner.Endings(), queryrunner.CutOffs())
	runner.WatchAdmission(func() (int, int) { return 0, 0 })

	families, err := prom.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	series := exportedSeries{}
	for _, family := range families {
		name := family.GetName()
		// A histogram is read as three series, a summary as itself and two.
		names := map[string][]string{name: nil}
		switch family.GetType().String() {
		case "HISTOGRAM":
			names = map[string][]string{name + "_bucket": {"le"}, name + "_sum": nil, name + "_count": nil}
		case "SUMMARY":
			names = map[string][]string{name: {"quantile"}, name + "_sum": nil, name + "_count": nil}
		}
		for seriesName, extra := range names {
			labels := map[string]map[string]bool{}
			for _, label := range extra {
				labels[label] = map[string]bool{}
			}
			for _, m := range family.GetMetric() {
				for _, pair := range m.GetLabel() {
					if labels[pair.GetName()] == nil {
						labels[pair.GetName()] = map[string]bool{}
					}
					labels[pair.GetName()][pair.GetValue()] = true
				}
			}
			series[seriesName] = labels
		}
	}
	// Made by Prometheus for every target it scrapes.
	series["up"] = map[string]map[string]bool{}
	return series
}

// problems says what is wrong with one selector. A recorded name comes from
// the rules file itself; its closed labels still carry the code's values.
func (e exportedSeries) problems(s selector, recorded map[string]bool) []string {
	labels, exported := e[s.name]
	switch {
	case recorded[s.name]:
		labels = e.closedValues()
	case !exported && strings.HasPrefix(s.name, "process_") && runtime.GOOS != "linux":
		// The process collector exports a different set per platform, and
		// production runs on Linux, where it exports them all.
		return nil
	case !exported:
		return []string{fmt.Sprintf("%s is not a series the API or the Query Runner exports", s.name)}
	}

	var found []string
	for _, m := range s.matchers {
		if scrapeLabels[m.label] {
			continue
		}
		values, ok := labels[m.label]
		switch {
		case !ok && !recorded[s.name]:
			found = append(found, fmt.Sprintf("%s has no label %s", s.name, m.label))
		case closedLabels[m.label] && m.op == "=" && !values[m.value]:
			found = append(found, fmt.Sprintf("%s{%s=%q}: %q is not a value the code declares", s.name, m.label, m.value, m.value))
		}
	}
	return found
}

// closedValues is every value each closed label takes on any series.
func (e exportedSeries) closedValues() map[string]map[string]bool {
	union := map[string]map[string]bool{}
	for _, labels := range e {
		for label, values := range labels {
			if !closedLabels[label] {
				continue
			}
			if union[label] == nil {
				union[label] = map[string]bool{}
			}
			for v := range values {
				union[label][v] = true
			}
		}
	}
	return union
}

func TestObservabilityImportsReadOnlyRegisteredSeries(t *testing.T) {
	series := registeredSeries(t)

	records, err := yamlScalars(rulesFile, "record")
	if err != nil {
		t.Fatal(err)
	}
	recorded := map[string]bool{}
	for _, name := range records {
		recorded[name] = true
	}
	ruleExprs, err := yamlScalars(rulesFile, "expr")
	if err != nil {
		t.Fatal(err)
	}
	testInputs, err := yamlScalars(rulesTestFile, "series")
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := dashboardQueries(dashboardFile)
	if err != nil {
		t.Fatal(err)
	}

	sources := []struct {
		name     string
		exprs    []string
		recorded map[string]bool
	}{
		{name: "rules", exprs: ruleExprs, recorded: recorded},
		// The tests' input series stand in for real scrapes, so they get no
		// recorded names and must use the code's own label values.
		{name: "rule tests' input series", exprs: testInputs},
		{name: "dashboard", exprs: dashboard},
	}
	for _, source := range sources {
		t.Run(source.name, func(t *testing.T) {
			read := 0
			for _, expr := range source.exprs {
				selectors, err := promqlSelectors(expr)
				if err != nil {
					t.Errorf("%v", err)
					continue
				}
				read += len(selectors)
				for _, s := range selectors {
					for _, p := range series.problems(s, source.recorded) {
						t.Errorf("%s\n\tin %s", p, expr)
					}
				}
			}
			if read == 0 {
				t.Fatalf("no selector read from %d expressions: the reader, not the file, is broken", len(source.exprs))
			}
		})
	}

	if *dashboardRules != "" {
		if err := writeDashboardRules(*dashboardRules, dashboard); err != nil {
			t.Fatal(err)
		}
	}
}

// grafanaVariable is a template variable in a dashboard query.
var grafanaVariable = regexp.MustCompile(`\$\{?(\w+)\}?`)

// writeDashboardRules writes each query as a recording rule, with Grafana's
// variables replaced by values PromQL accepts, so promtool can parse them.
func writeDashboardRules(path string, queries []string) error {
	var b strings.Builder
	b.WriteString("groups:\n  - name: dashboard\n    rules:\n")
	for i, q := range queries {
		q = grafanaVariable.ReplaceAllStringFunc(q, func(v string) string {
			if strings.Contains(v, "interval") {
				return "5m"
			}
			return "x"
		})
		fmt.Fprintf(&b, "      - record: dashboard:query_%d\n        expr: '%s'\n", i+1, strings.ReplaceAll(q, "'", "''"))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func TestPromQLSelectorsReadMetricsNotSyntax(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want []selector
	}{
		{
			name: "aggregation with labels first",
			expr: `sum by (outcome) (rate(queryrunner_queries_total{job=~"$runner_job", outcome="ok"}[$__rate_interval])) * 60`,
			want: []selector{{name: "queryrunner_queries_total", matchers: []matcher{
				{label: "job", op: "=~", value: "$runner_job"}, {label: "outcome", op: "=", value: "ok"}}}},
		},
		{
			name: "vector matching modifiers",
			expr: `up * on (job, instance) group_left (dbcontest) dbcontest:target{dbcontest="api"} == 0`,
			want: []selector{{name: "up"}, {name: "dbcontest:target", matchers: []matcher{
				{label: "dbcontest", op: "=", value: "api"}}}},
		},
		{
			name: "labels last, strings and offset",
			expr: `histogram_quantile(0.95, sum(rate(a_bucket{route!~".*/(query|x\\.csv)"}[5m] offset 1h)) by (le)) / label_replace(b, "d", "$1", "s", "(.).*")`,
			want: []selector{
				{name: "a_bucket", matchers: []matcher{{label: "route", op: "!~", value: `.*/(query|x\.csv)`}}},
				{name: "b"},
			},
		},
		{
			name: "keywords between selectors",
			expr: "a > 0.05\nand b * 300 >= 10 unless c",
			want: []selector{{name: "a"}, {name: "b"}, {name: "c"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := promqlSelectors(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("promqlSelectors(%q)\n got %+v\nwant %+v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestPromQLSelectorsRefuseWhatTheyCannotRead(t *testing.T) {
	for _, expr := range []string{
		`{__name__="a"}`,
		`rate(a[5m]`,
		`a{b="c"`,
		`a{b=c}`,
		`rate(a[$__rate_interval]) * $x`,
	} {
		if got, err := promqlSelectors(expr); err == nil {
			t.Errorf("promqlSelectors(%q) = %+v, want an error", expr, got)
		}
	}
}
