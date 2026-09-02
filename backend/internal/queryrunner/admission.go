package queryrunner

import (
	"context"
	"errors"
	"sync"
)

// What admission control answers with when it will not admit a query. Both are
// ordinary outcomes rather than faults: they are what the participant is told,
// and what the journal records.
var (
	// ErrBusy means the instance is full and the queue is too. Answered
	// immediately rather than waited out — a request that hangs teaches the
	// participant nothing and keeps the slot spoken for.
	ErrBusy = errors.New("the system is busy")
	// ErrAlreadyRunning means this participant already has a query in flight.
	ErrAlreadyRunning = errors.New("a query is already running")
)

// gate is admission control: how many queries may run at once, and how many
// may wait.
//
// It exists because statement_timeout bounds one query and nothing bounds the
// sum. Two hundred participants running one heavy query each will bring an
// instance down with every per-query limit in place, so the limit that matters
// is on how many run together (section 4.3).
type gate struct {
	slots chan struct{}

	mu      sync.Mutex
	waiting int
	running map[string]struct{}
	depth   int
}

func newGate(concurrent, depth int) *gate {
	if concurrent < 1 {
		concurrent = 1
	}
	if depth < 0 {
		depth = 0
	}
	return &gate{
		slots:   make(chan struct{}, concurrent),
		running: make(map[string]struct{}),
		depth:   depth,
	}
}

// enter admits a participant's query, or explains why not.
//
// Two limits, checked in this order. One query per participant comes first
// because it is about who is asking and can be answered without touching the
// semaphore. Then a slot: taken at once if one is free, waited for if there is
// room in the queue, and refused immediately if there is not.
//
// The returned function must be called however the execution ended. A slot
// that is not returned is gone for the life of the process, and an instance
// that leaks one per failure stops admitting anything after N failures.
func (g *gate) enter(ctx context.Context, participant string) (func(), error) {
	g.mu.Lock()
	if _, already := g.running[participant]; already {
		g.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	g.running[participant] = struct{}{}
	g.mu.Unlock()

	release := func() {
		<-g.slots
		g.mu.Lock()
		delete(g.running, participant)
		g.mu.Unlock()
	}

	// A slot going spare: take it without joining the queue at all.
	select {
	case g.slots <- struct{}{}:
		return release, nil
	default:
	}

	g.mu.Lock()
	if g.waiting >= g.depth {
		delete(g.running, participant)
		g.mu.Unlock()
		return nil, ErrBusy
	}
	g.waiting++
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
	}()

	select {
	case g.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		g.mu.Lock()
		delete(g.running, participant)
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}
