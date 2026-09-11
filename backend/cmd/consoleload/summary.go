package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// latency is a distribution of durations, in milliseconds.
type latency struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

// distribution summarises durations by nearest rank: the p-th percentile is
// the smallest value at least p percent of the sample is no larger than. With
// a few hundred samples that is a value somebody actually waited, rather
// than an interpolation between two that nobody did.
func distribution(values []time.Duration) latency {
	if len(values) == 0 {
		return latency{}
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(p float64) float64 {
		i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
		i = max(0, min(i, len(sorted)-1))
		return ms(sorted[i])
	}
	return latency{
		Count: len(sorted),
		P50:   rank(50), P95: rank(95), P99: rank(99),
		Max: ms(sorted[len(sorted)-1]),
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// runSummary is one run, as the report shows it.
type runSummary struct {
	Name         string    `json:"name"`
	Shape        string    `json:"shape"`
	Participants int       `json:"participants"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`

	Queries int `json:"queries"`
	// Outcomes counts every answer and refusal by status and code.
	Outcomes map[string]int `json:"outcomes"`

	// All is every query's latency, refusals included — a 503 is a wait the
	// participant sat through too, however short. Answered is only the ones
	// that came back with rows, and ByKind splits those by query class.
	All      latency          `json:"latency_all"`
	Answered latency          `json:"latency_answered"`
	ByKind   map[Kind]latency `json:"latency_answered_by_kind"`
	// Statement is the Query Runner's own measurement of the statement alone,
	// from the answer: no queue, no connection, no journal, no HTTP.
	Statement latency `json:"statement"`
	// Journal is the query log's view of the same queries: the time from
	// the API opening the journal row to closing it — the gRPC call, the
	// queue in front of the execution slots, the connection and the
	// statement — by the status the journal recorded.
	Journal journalSummary `json:"journal"`

	Usage    map[string]usage `json:"usage"`
	Backends backends         `json:"game_backends"`
	// InFlightMax is the most queries the participants had sent and not yet
	// had answered at one moment.
	InFlightMax int `json:"in_flight_max"`
}

type journalSummary struct {
	ByStatus map[string]int `json:"by_status"`
	P50      float64        `json:"p50_ms"`
	P95      float64        `json:"p95_ms"`
	P99      float64        `json:"p99_ms"`
	Max      float64        `json:"max_ms"`
}

func summarise(name, shape string, participants int, started, ended time.Time, outcomes []outcome) runSummary {
	s := runSummary{
		Name: name, Shape: shape, Participants: participants, Started: started, Ended: ended,
		Queries: len(outcomes), Outcomes: map[string]int{}, ByKind: map[Kind]latency{},
	}
	var all, answered, statement []time.Duration
	byKind := map[Kind][]time.Duration{}
	for _, o := range outcomes {
		s.Outcomes[o.label()]++
		all = append(all, o.Latency)
		if o.ok() {
			answered = append(answered, o.Latency)
			byKind[o.Kind] = append(byKind[o.Kind], o.Latency)
			statement = append(statement, time.Duration(o.StatementMicros)*time.Microsecond)
		}
	}
	s.All, s.Answered, s.Statement = distribution(all), distribution(answered), distribution(statement)
	for kind, values := range byKind {
		s.ByKind[kind] = distribution(values)
	}
	return s
}

// readJournal reads what the query log recorded for these registrations
// between two instants.
func readJournal(ctx context.Context, core *pgxpool.Pool, registrations []uuid.UUID, from, to time.Time) (journalSummary, error) {
	j := journalSummary{ByStatus: map[string]int{}}
	rows, err := core.Query(ctx, `
		SELECT status, count(*) FROM query_log
		WHERE registration_id = ANY($1) AND executed_at BETWEEN $2 AND $3
		GROUP BY status`, registrations, from, to)
	if err != nil {
		return j, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return j, err
		}
		j.ByStatus[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return j, err
	}

	var p50, p95, p99, top *float64
	err = core.QueryRow(ctx, `
		SELECT (percentile_disc(0.50) WITHIN GROUP (ORDER BY duration_ms))::float8,
		       (percentile_disc(0.95) WITHIN GROUP (ORDER BY duration_ms))::float8,
		       (percentile_disc(0.99) WITHIN GROUP (ORDER BY duration_ms))::float8,
		       max(duration_ms)::float8
		FROM query_log
		WHERE registration_id = ANY($1) AND executed_at BETWEEN $2 AND $3
		  AND duration_ms IS NOT NULL`, registrations, from, to).Scan(&p50, &p95, &p99, &top)
	if err != nil {
		return j, err
	}
	for _, pair := range []struct {
		dst *float64
		src *float64
	}{{&j.P50, p50}, {&j.P95, p95}, {&j.P99, p99}, {&j.Max, top}} {
		if pair.src != nil {
			*pair.dst = *pair.src
		}
	}
	return j, nil
}

// markdown renders the runs as the tables the report is made of.
func markdown(runs []runSummary) string {
	var b strings.Builder

	b.WriteString("### Latency as the participant sees it (ms)\n\n")
	b.WriteString("| run | queries | p50 | p95 | p99 | max | answered p50 | answered p95 | answered p99 | statement p50 | statement p95 | statement p99 |\n")
	b.WriteString("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f |\n",
			r.Name, r.Queries, r.All.P50, r.All.P95, r.All.P99, r.All.Max,
			r.Answered.P50, r.Answered.P95, r.Answered.P99,
			r.Statement.P50, r.Statement.P95, r.Statement.P99)
	}

	b.WriteString("\n### Answered latency by query class (ms, p50 / p95 / p99, count)\n\n")
	b.WriteString("| run | cheap | mid | expensive |\n|---|---|---|---|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s", r.Name)
		for _, kind := range kinds {
			l := r.ByKind[kind]
			fmt.Fprintf(&b, " | %.0f / %.0f / %.0f (%d)", l.P50, l.P95, l.P99, l.Count)
		}
		b.WriteString(" |\n")
	}

	b.WriteString("\n### Outcomes\n\n| run | outcomes | journal (status: count) | journal p50 / p95 / p99 / max (ms) |\n|---|---|---|---|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s | %s | %s | %.0f / %.0f / %.0f / %.0f |\n",
			r.Name, joinCounts(r.Outcomes), joinCounts(r.Journal.ByStatus),
			r.Journal.P50, r.Journal.P95, r.Journal.P99, r.Journal.Max)
	}

	b.WriteString("\n### Resources during the run\n\n")
	b.WriteString("| run | watched | CPU avg % | CPU max % | memory max | read from disk | samples |\n|---|---|---:|---:|---:|---:|---:|\n")
	for _, r := range runs {
		for _, name := range sortedKeys(r.Usage) {
			u := r.Usage[name]
			fmt.Fprintf(&b, "| %s | %s | %.1f | %.0f | %s | %s | %d |\n",
				r.Name, name, u.CPUAvg, u.CPUMax, human(u.MemMax), human(u.ReadBytes), u.Samples)
		}
	}

	b.WriteString("\n### Queries the game cluster was running at once\n\n")
	b.WriteString("| run | sent, not yet answered, max | queries active max | queries active avg | connections max | parallel workers max |\n|---|---:|---:|---:|---:|---:|\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "| %s | %d | %d | %.1f | %d | %d |\n", r.Name, r.InFlightMax, r.Backends.ActiveMax, r.Backends.ActiveAvg,
			r.Backends.ConnectMax, r.Backends.WorkersMax)
	}
	return b.String()
}

func joinCounts(m map[string]int) string {
	parts := make([]string, 0, len(m))
	for _, key := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s: %d", key, m[key]))
	}
	return strings.Join(parts, ", ")
}

// human prints a byte count in binary units.
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n), 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGT"[exp-1])
}
