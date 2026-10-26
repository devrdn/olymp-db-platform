package queryrunner

import (
	"fmt"
	"sync"
	"time"
)

// slotBudgetPerMinute is how much of each minute one participant may hold an
// execution slot: this many deadlines. The gate bounds how many queries run
// at once and the rate how many one participant may send, but neither bounds
// the time a query holds a slot. A statement that runs to its deadline costs
// its author one admission and everybody else a slot for the whole deadline:
// eight participants each repeating one such statement (a Cartesian join is
// enough, no malice is needed) would hold all eight slots of the default
// installation for the rest of the contest, and every other query would wait
// in the queue until it was told it timed out. Charging the time to the
// participant who spent it leaves such a participant a slot for a fraction
// of the minute instead of all of it. Two deadlines a minute lets anybody run
// a slow query twice before being asked to wait; a legitimate query on a
// contest game answers in well under a second.
const slotBudgetPerMinute = 2

// slotBudget is a sliding sum, per participant, of the time their queries
// held a slot over the last minute. In memory, like the rate window, because
// one Query Runner is one process.
type slotBudget struct {
	limit time.Duration
	over  time.Duration
	now   func() time.Time

	mu    sync.Mutex
	spent map[string][]charge
	since int
}

// charge is one query's time, as of when it ended.
type charge struct {
	at   time.Time
	cost time.Duration
}

// newSlotBudget allows each participant limit of slot time per over. A limit
// of zero or less allows everything.
func newSlotBudget(limit, over time.Duration, now func() time.Time) *slotBudget {
	if now == nil {
		now = time.Now
	}
	return &slotBudget{limit: limit, over: over, now: now, spent: map[string][]charge{}}
}

// allow reports whether the participant is within their budget. It records
// nothing itself: what counts is time spent, which spend charges once known,
// so a refused participant retrying does not extend their wait.
func (b *slotBudget) allow(participant string) error {
	if b.limit <= 0 {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if b.since++; b.since >= pruneEvery {
		b.since = 0
		b.prune(now)
	}
	cutoff := now.Add(-b.over)

	var total time.Duration
	kept := b.spent[participant][:0]
	for _, c := range b.spent[participant] {
		if c.at.After(cutoff) {
			kept = append(kept, c)
			total += c.cost
		}
	}
	if len(kept) == 0 {
		delete(b.spent, participant)
	} else {
		b.spent[participant] = kept
	}

	if total >= b.limit {
		return fmt.Errorf("%w: %s of execution time in the last %s", ErrTooManyQueries,
			total.Round(time.Millisecond), b.over)
	}
	return nil
}

// spend charges the participant for the time one query held a slot. The list
// stays bounded: a charge is added only for an admitted query, and the rate
// bounds admissions.
func (b *slotBudget) spend(participant string, cost time.Duration) {
	if b.limit <= 0 || cost <= 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent[participant] = append(b.spent[participant], charge{at: b.now(), cost: cost})
}

// prune drops the participants whose charges have all aged out, so the map
// does not grow with everyone who ever ran a query.
func (b *slotBudget) prune(now time.Time) {
	cutoff := now.Add(-b.over)
	for participant, charges := range b.spent {
		live := false
		for _, c := range charges {
			if c.at.After(cutoff) {
				live = true
				break
			}
		}
		if !live {
			delete(b.spent, participant)
		}
	}
}
