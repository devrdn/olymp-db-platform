package main

import (
	"testing"
	"time"
)

func TestDistributionIsNearestRank(t *testing.T) {
	var values []time.Duration
	for i := 100; i >= 1; i-- {
		values = append(values, time.Duration(i)*time.Millisecond)
	}
	got := distribution(values)
	want := latency{Count: 100, P50: 50, P95: 95, P99: 99, Max: 100}
	if got != want {
		t.Fatalf("distribution = %+v, want %+v", got, want)
	}
}

func TestDistributionOfFewValuesReportsTheWorst(t *testing.T) {
	// With fewer than a hundred samples p99 is the maximum.
	got := distribution([]time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond})
	if got.P99 != 3 || got.P50 != 2 {
		t.Fatalf("distribution = %+v, want p50 2 and p99 3", got)
	}
	if empty := distribution(nil); empty != (latency{}) {
		t.Fatalf("distribution(nil) = %+v, want zero", empty)
	}
}

func TestSummariseCountsRefusalsAndKeepsThemOutOfAnswered(t *testing.T) {
	outcomes := []outcome{
		{Kind: KindCheap, Status: 200, Latency: 10 * time.Millisecond, StatementMicros: 2000},
		{Kind: KindExpensive, Status: 200, Latency: 900 * time.Millisecond, StatementMicros: 800000},
		{Kind: KindMid, Status: 503, Code: "query_busy", Latency: 2 * time.Millisecond},
		{Kind: KindMid, Latency: time.Second, Err: "connection refused"},
	}
	s := summarise("burst-2", shapeBurst, 2, time.Time{}, time.Time{}, outcomes)

	if s.Queries != 4 || s.All.Count != 4 || s.Answered.Count != 2 {
		t.Fatalf("counts = %d queries, %d all, %d answered; want 4, 4, 2", s.Queries, s.All.Count, s.Answered.Count)
	}
	for label, want := range map[string]int{"200": 2, "503 query_busy": 1, "no response": 1} {
		if s.Outcomes[label] != want {
			t.Errorf("outcomes[%q] = %d, want %d (all: %v)", label, s.Outcomes[label], want, s.Outcomes)
		}
	}
	if _, refused := s.ByKind[KindMid]; refused {
		t.Errorf("a class with no answers has a latency: %+v", s.ByKind)
	}
	if s.Statement.Max != 800 {
		t.Errorf("statement max = %v ms, want 800", s.Statement.Max)
	}
}

func TestSummariseSortsEveryQueryIntoOneClass(t *testing.T) {
	outcomes := []outcome{
		{Status: 200},
		{Status: 200, Err: "unreadable answer: unexpected end of JSON input"},
		{Status: 400, Code: "query_database_error"},
		{Status: 429, Code: "query_too_often"},
		{Status: 500, Code: "internal_error"},
		{Status: 503, Code: "query_busy"},
		{Status: 504, Code: "query_timed_out"},
		{Err: "connection reset by peer"},
	}
	s := summarise("steady-8", shapeSteady, 8, time.Time{}, time.Time{}, outcomes)

	got := [4]int{s.Queries - s.Refused - s.ServerErrors - s.Errors, s.Refused, s.ServerErrors, s.Errors}
	if want := [4]int{1, 2, 3, 2}; got != want {
		t.Fatalf("answered, refused, server errors, errors = %v, want %v", got, want)
	}
}
