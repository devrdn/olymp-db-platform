package main

import (
	"math/rand/v2"
	"testing"
)

func TestParseMix(t *testing.T) {
	m, err := parseMix("50, 30,20")
	if err != nil {
		t.Fatal(err)
	}
	if m[KindCheap] != 50 || m[KindMid] != 30 || m[KindExpensive] != 20 {
		t.Fatalf("parseMix = %v", m)
	}
	for _, bad := range []string{"50,30", "50,30,30", "a,b,c", "110,-10,0"} {
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
