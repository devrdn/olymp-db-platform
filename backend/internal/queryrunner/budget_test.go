package queryrunner

import (
	"errors"
	"testing"
	"time"
)

func TestASlotBudgetRefusesAParticipantWhoSpentItAndForgetsPast(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	b := newSlotBudget(10*time.Second, time.Minute, c.now)

	// Two queries that ran to a five-second deadline.
	for i := range 2 {
		if err := b.allow("p"); err != nil {
			t.Fatalf("query %d was refused: %v", i+1, err)
		}
		b.spend("p", 5*time.Second)
		c.tick(time.Second)
	}
	if err := b.allow("p"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatalf("with the budget spent: %v, want ErrTooManyQueries", err)
	}

	// A minute after the first charge it no longer counts, and the second
	// alone leaves room for one more query, but only one.
	c.tick(58*time.Second + 500*time.Millisecond)
	if err := b.allow("p"); err != nil {
		t.Fatalf("after the first charge aged out: %v", err)
	}
	b.spend("p", 5*time.Second)
	if err := b.allow("p"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatalf("with the second and third charges in the window: %v, want ErrTooManyQueries", err)
	}
}

func TestQuickQueriesNeverReachTheSlotBudget(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	b := newSlotBudget(10*time.Second, time.Minute, c.now)

	for i := range 60 {
		if err := b.allow("p"); err != nil {
			t.Fatalf("query %d was refused: %v", i+1, err)
		}
		b.spend("p", 20*time.Millisecond)
		c.tick(time.Second)
	}
}

func TestTheSlotBudgetIsPerParticipant(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	b := newSlotBudget(10*time.Second, time.Minute, c.now)

	b.spend("first", 10*time.Second)
	if err := b.allow("first"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatalf("first: %v, want ErrTooManyQueries", err)
	}
	if err := b.allow("second"); err != nil {
		t.Fatalf("second: %v", err)
	}
}

// A refused participant retrying must not be charged for asking.
func TestAskingSpendsNothing(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	b := newSlotBudget(10*time.Second, time.Minute, c.now)

	b.spend("p", 10*time.Second)
	for range 100 {
		if err := b.allow("p"); !errors.Is(err, ErrTooManyQueries) {
			t.Fatalf("got %v, want ErrTooManyQueries", err)
		}
	}
	c.tick(61 * time.Second)
	if err := b.allow("p"); err != nil {
		t.Fatalf("after the window, however often it asked: %v", err)
	}
}

func TestANilOrZeroBudgetAllowsEverything(t *testing.T) {
	b := newSlotBudget(0, time.Minute, nil)
	b.spend("p", time.Hour)
	if err := b.allow("p"); err != nil {
		t.Fatalf("a zero budget refused: %v", err)
	}
}

func TestTheSlotBudgetPrunesTheForgotten(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	b := newSlotBudget(10*time.Second, time.Minute, c.now)

	b.spend("gone", time.Second)
	c.tick(2 * time.Minute)
	for range pruneEvery {
		if err := b.allow("other"); err != nil {
			t.Fatal(err)
		}
	}
	if _, held := b.spent["gone"]; held {
		t.Fatal("a participant whose charges all aged out is still held")
	}
}
