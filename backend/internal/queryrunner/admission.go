package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Admission refusals: ordinary outcomes the participant is told, not faults.
var (
	// ErrBusy means every slot and the queue are full. It is answered at once
	// rather than waited out.
	ErrBusy = errors.New("the system is busy")
	// ErrAlreadyRunning means this participant already has a query in flight.
	ErrAlreadyRunning = errors.New("a query is already running")
)

// gate is admission control: how many queries may run at once, and how many
// may wait. Per-query limits do not bound the sum of many heavy queries
// (section 4.3).
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

// enter admits a participant's query: one per participant first, then a slot,
// taken at once, waited for if the queue has room, or refused.
//
// The returned release must be called however the execution ended; a leaked
// slot is gone for the life of the process.
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
		// Named, because a bare context error would be reported as a
		// database fault.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: waiting for a free slot", ErrTimeout)
		}
		return nil, fmt.Errorf("%w: while waiting for a free slot", ErrCanceled)
	}
}
