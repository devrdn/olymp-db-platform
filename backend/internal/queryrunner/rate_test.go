package queryrunner

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// A fixed clock, because a rate limit tested by waiting is a test that is slow
// when it passes and flaky when the machine is busy.
type clock struct{ at time.Time }

func (c *clock) now() time.Time       { return c.at }
func (c *clock) tick(d time.Duration) { c.at = c.at.Add(d) }

func TestARateLimitCountsWithinItsWindowAndForgetsPast(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	w := newWindow(3, time.Minute, c.now)

	for i := range 3 {
		if err := w.admit("p"); err != nil {
			t.Fatalf("query %d was refused: %v", i+1, err)
		}
	}
	if err := w.admit("p"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatalf("the fourth query in a minute: %v, want ErrTooManyQueries", err)
	}

	// Past the window the earliest no longer counts.
	c.tick(61 * time.Second)
	if err := w.admit("p"); err != nil {
		t.Fatalf("a query after the window: %v", err)
	}
}

// One participant hitting the limit must not slow anybody else down.
func TestTheRateIsPerParticipant(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	w := newWindow(1, time.Minute, c.now)

	if err := w.admit("first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := w.admit("first"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatal("the first participant was not limited")
	}
	if err := w.admit("second"); err != nil {
		t.Fatalf("another participant was caught by somebody else's limit: %v", err)
	}
}

// A refused query is not counted, or somebody who keeps clicking extends their
// own penalty for ever — which turns slowing them down into removing them from
// the contest.
func TestARefusedQueryDoesNotExtendThePenalty(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	w := newWindow(1, time.Minute, c.now)

	_ = w.admit("p")
	for range 20 {
		c.tick(time.Second)
		_ = w.admit("p") // all refused
	}

	// The single accepted query was 21 seconds ago; the window is a minute, so
	// it still counts and the next is still refused...
	if err := w.admit("p"); !errors.Is(err, ErrTooManyQueries) {
		t.Fatal("the limit lapsed early")
	}
	// ...but only until it ages out, which the refusals did not postpone.
	c.tick(40 * time.Second)
	if err := w.admit("p"); err != nil {
		t.Fatalf("the penalty outlived the window: %v", err)
	}
}

func TestNoLimitMeansNoLimit(t *testing.T) {
	w := newWindow(0, time.Minute, nil)
	for range 100 {
		if err := w.admit("p"); err != nil {
			t.Fatalf("an unlimited window refused: %v", err)
		}
	}
}

// The map must not keep a key for everybody who ever asked anything: a process
// meant to run for months across a term of olympiads would leak one per
// participant.
func TestTheWindowForgetsParticipantsItNoLongerCounts(t *testing.T) {
	c := &clock{at: time.Unix(1_700_000_000, 0)}
	w := newWindow(10, time.Minute, c.now)

	for i := range 300 {
		_ = w.admit("participant-" + strconv.Itoa(i))
	}
	// Everybody's single query is now well outside the window.
	c.tick(2 * time.Minute)
	for range pruneEvery {
		_ = w.admit("someone-else")
	}

	w.mu.Lock()
	remaining := len(w.seen)
	w.mu.Unlock()

	if remaining > 10 {
		t.Fatalf("%d participants still counted after their queries aged out", remaining)
	}
}
