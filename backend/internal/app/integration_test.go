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

	"github.com/devrdn/db-contest/backend/internal/mailing"
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

// closedLabels take only values the code declares and exports at zero from the
// start, so an equality matcher on one can be held to the code.
// route, status and method take whatever the traffic brings.
var closedLabels = map[string]bool{"outcome": true, "reason": true, "limit": true}

// The two services, named as the selector's dbcontest label names them.
const (
	serviceAPI    = "api"
	serviceRunner = "queryrunner"
)

// dashboardVariables names the service each of the dashboard's job variables
// selects.
var dashboardVariables = map[string]string{"api_job": serviceAPI, "runner_job": serviceRunner}

// exportedSeries maps each series name one service exports to its labels and
// the values seen for each.
type exportedSeries map[string]map[string]map[string]bool

// registeredSeries builds each service's metric sets with the constructors its
// composition root uses, on a registry of its own, so a query can be held to
// the service it claims to read.
func registeredSeries(t *testing.T) map[string]exportedSeries {
	t.Helper()

	api := metrics.NewPrometheus()
	// The HTTP vectors export nothing until a request is observed.
	api.ObserveRequest("GET", "/api/v1/contests", 200, time.Millisecond, false)
	api.ObserveRequest("GET", "/api/v1/contests/{contestID}/events", 200, time.Minute, true)
	metrics.NewGameReclaimCounters(api)
	metrics.NewMailCounters(api, mailing.KindNames(), mailing.Outcomes())

	runner := metrics.NewPrometheus()
	observed := metrics.NewQueryRunner(runner, queryrunner.Endings(), queryrunner.CutOffs())
	observed.WatchAdmission(func() (int, int) { return 0, 0 })

	return map[string]exportedSeries{serviceAPI: gathered(t, api), serviceRunner: gathered(t, runner)}
}

func gathered(t *testing.T, prom *metrics.Prometheus) exportedSeries {
	t.Helper()

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

// selectorJob reads the job each service's matcher names in the rules'
// selector, label_replace(... up{job="core-api"} ...), "dbcontest", "api", ...
var selectorJob = regexp.MustCompile(`up\{job="([^"]+)"\}\), "dbcontest", "(\w+)"`)

// servicesOf names the services a selector reads: from its job matcher when
// that names a dashboard variable or a job the rules' selector names, from the
// expression's dbcontest:target join otherwise, and none when neither says.
func servicesOf(s selector, expr string, jobs map[string]string) []string {
	found := map[string]bool{}
	for _, m := range s.matchers {
		if m.label != "job" {
			continue
		}
		for _, part := range strings.Split(m.value, "|") {
			if service, ok := jobs[strings.Trim(part, "$")]; ok {
				found[service] = true
			}
		}
	}
	if len(found) == 0 {
		for _, service := range []string{serviceAPI, serviceRunner} {
			if strings.Contains(expr, `dbcontest="`+service+`"`) {
				found[service] = true
			}
		}
	}
	var services []string
	for _, service := range []string{serviceAPI, serviceRunner} {
		if found[service] {
			services = append(services, service)
		}
	}
	return services
}

// problems says what is wrong with one selector read from services, or from
// either when services is empty. A recorded name comes from the rules file
// itself; its closed labels still carry the code's values.
func problems(registries map[string]exportedSeries, s selector, services []string, recorded map[string]bool) []string {
	if recorded[s.name] {
		return matcherProblems(s, closedValues(registries), true)
	}
	if !registries[serviceAPI].has(s.name) && !registries[serviceRunner].has(s.name) &&
		strings.HasPrefix(s.name, "process_") && runtime.GOOS != "linux" {
		// The process collector exports a different set per platform, and
		// production runs on Linux, where it exports them all.
		return nil
	}

	if len(services) == 0 {
		for _, service := range []string{serviceAPI, serviceRunner} {
			if registries[service].has(s.name) {
				return matcherProblems(s, registries[service][s.name], false)
			}
		}
		return []string{fmt.Sprintf("%s is not a series the API or the Query Runner exports", s.name)}
	}
	var found []string
	for _, service := range services {
		if !registries[service].has(s.name) {
			found = append(found, fmt.Sprintf("%s is not a series the %s exports", s.name, service))
			continue
		}
		found = append(found, matcherProblems(s, registries[service][s.name], false)...)
	}
	return found
}

func (e exportedSeries) has(name string) bool {
	_, ok := e[name]
	return ok
}

func matcherProblems(s selector, labels map[string]map[string]bool, recorded bool) []string {
	var found []string
	for _, m := range s.matchers {
		if scrapeLabels[m.label] {
			continue
		}
		values, ok := labels[m.label]
		switch {
		case !ok && !recorded:
			found = append(found, fmt.Sprintf("%s has no label %s", s.name, m.label))
		case closedLabels[m.label] && m.op == "=" && !values[m.value]:
			found = append(found, fmt.Sprintf("%s{%s=%q}: %q is not a value the code declares", s.name, m.label, m.value, m.value))
		}
	}
	return found
}

// closedValues is every value each closed label takes on any series.
func closedValues(registries map[string]exportedSeries) map[string]map[string]bool {
	union := map[string]map[string]bool{}
	for _, series := range registries {
		for _, labels := range series {
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
	}
	return union
}

func TestObservabilityImportsReadOnlyRegisteredSeries(t *testing.T) {
	registries := registeredSeries(t)

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
	dashboard, variables, err := dashboardQueries(dashboardFile)
	if err != nil {
		t.Fatal(err)
	}

	jobs := map[string]string{}
	for variable, service := range dashboardVariables {
		jobs[variable] = service
	}
	for _, expr := range ruleExprs {
		for _, m := range selectorJob.FindAllStringSubmatch(expr, -1) {
			jobs[m[1]] = m[2]
		}
	}
	if len(jobs) != len(dashboardVariables)+2 {
		t.Fatalf("the rules' selector should name one job per service, found %v", jobs)
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
					for _, p := range problems(registries, s, servicesOf(s, expr, jobs), source.recorded) {
						t.Errorf("%s\n\tin %s", p, expr)
					}
				}
			}
			if read == 0 {
				t.Fatalf("no selector read from %d expressions: the reader, not the file, is broken", len(source.exprs))
			}
		})
	}

	// Each job variable is found by a metric only its service exports, or it
	// would offer the other service's job too.
	t.Run("dashboard variables", func(t *testing.T) {
		for variable, metric := range variables {
			service, ok := dashboardVariables[variable]
			if !ok {
				t.Errorf("variable %s selects no known service", variable)
				continue
			}
			for other, series := range registries {
				if exports := series.has(metric); exports != (other == service) {
					t.Errorf("variable %s reads %s: exported by %s = %v", variable, metric, other, exports)
				}
			}
		}
		if len(variables) != len(dashboardVariables) {
			t.Errorf("dashboard has job variables %v, want %v", variables, dashboardVariables)
		}
	})

	if *dashboardRules != "" {
		if err := writeDashboardRules(*dashboardRules, dashboard); err != nil {
			t.Fatal(err)
		}
	}
}

// The routes left out of the API's latency as slow by design are named in the
// rules and again in the dashboard; one pattern in both is what makes the panel
// show what the alert measures.
func TestObservabilityImportsLeaveOutTheSameSlowRoutes(t *testing.T) {
	ruleExprs, err := yamlScalars(rulesFile, "expr")
	if err != nil {
		t.Fatal(err)
	}
	dashboard, _, err := dashboardQueries(dashboardFile)
	if err != nil {
		t.Fatal(err)
	}

	excluded := func(exprs []string) map[string]bool {
		patterns := map[string]bool{}
		for _, expr := range exprs {
			selectors, err := promqlSelectors(expr)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range selectors {
				for _, m := range s.matchers {
					if m.label == "route" && m.op == "!~" {
						patterns[m.value] = true
					}
				}
			}
		}
		return patterns
	}
	inRules, inDashboard := excluded(ruleExprs), excluded(dashboard)
	if len(inRules) != 1 || !reflect.DeepEqual(inRules, inDashboard) {
		t.Errorf("slow routes left out: rules %v, dashboard %v; want one pattern, the same in both", inRules, inDashboard)
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
