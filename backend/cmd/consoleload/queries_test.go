package main

import (
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy/checker"
)

func TestParseMix(t *testing.T) {
	m, err := parseMix("50, 30,20")
	if err != nil {
		t.Fatal(err)
	}
	if m[KindCheap] != 50 || m[KindMid] != 30 || m[KindExpensive] != 20 {
		t.Fatalf("parseMix = %v", m)
	}
	if m[KindWrite] != 0 {
		t.Fatalf("three percentages gave writes a share: %v", m)
	}
	m, err = parseMix("40,30,20,10")
	if err != nil {
		t.Fatal(err)
	}
	if m[KindWrite] != 10 {
		t.Fatalf("parseMix with writes = %v", m)
	}
	for _, bad := range []string{"50,30", "50,30,30", "a,b,c", "110,-10,0", "40,30,20,20", "40,30,20,5,5"} {
		if _, err := parseMix(bad); err == nil {
			t.Errorf("parseMix(%q) accepted", bad)
		}
	}
}

func TestPickFollowsTheMix(t *testing.T) {
	m, _ := parseMix("50,30,20")
	r := rand.New(rand.NewPCG(1, 2))
	seen := map[Kind]int{}
	const draws = 20000
	for range draws {
		kind, sql := m.pick(r)
		if sql == "" {
			t.Fatalf("an empty %s query", kind)
		}
		seen[kind]++
	}
	// Within two percentage points of the requested share.
	for kind, share := range m {
		got := 100 * float64(seen[kind]) / draws
		if got < float64(share)-2 || got > float64(share)+2 {
			t.Errorf("%s drawn %.1f%% of the time, want about %d%%", kind, got, share)
		}
	}
}

func TestPickNeverDrawsAKindWithNoShare(t *testing.T) {
	m, _ := parseMix("100,0,0")
	r := rand.New(rand.NewPCG(3, 4))
	for range 1000 {
		if kind, _ := m.pick(r); kind != KindCheap {
			t.Fatalf("drew %s from a mix of only cheap queries", kind)
		}
	}
}

func TestWritesUpdateOnlyTheWritableTable(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	for _, write := range templates[KindWrite] {
		sql := write(r)
		if !strings.HasPrefix(sql, "UPDATE "+writableTable+" ") {
			t.Errorf("a write that is not an update of %s: %s", writableTable, sql)
		}
	}
}

// The Query Runner's own checker, under the policies setup saves: every
// query is admitted where the profile sends it, and a write nowhere else.
func TestTheQueryRunnersPolicyAdmitsEveryQuery(t *testing.T) {
	readWrite, readOnly := sqlpolicy.ReadWrite(writableTable), sqlpolicy.ReadOnly()
	r := rand.New(rand.NewPCG(9, 10))
	for _, kind := range kinds {
		for i, template := range templates[kind] {
			sql := template(r)
			if err := checker.Check(sql, readWrite); err != nil {
				t.Errorf("%s query %d refused in a read-write contest: %v\n%s", kind, i, err, sql)
			}
			err := checker.Check(sql, readOnly)
			if kind == KindWrite && err == nil {
				t.Errorf("write %d admitted in a read-only contest: %s", i, sql)
			}
			if kind != KindWrite && err != nil {
				t.Errorf("%s query %d refused in a read-only contest: %v\n%s", kind, i, err, sql)
			}
		}
	}
}
