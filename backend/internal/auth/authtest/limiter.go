// Package authtest holds stand-ins for auth's parts in the tests of other
// packages: a limiter whose windows a test ends at will, and one that admits
// everything. It is not for production use.
package authtest

import (
	"context"
	"sync"
	"time"
)

// Limiter counts attempts per subject as auth.Limiter does, in fixed
// windows, but lets a test end them with Pass instead of waiting.
type Limiter struct {
	mu     sync.Mutex
	counts map[string]*count
}

type count struct {
	n      int
	window time.Duration
}

// Spend counts the attempt, refusals included, and reports whether it is
// within limit and whether it is the window's first refusal.
func (l *Limiter) Spend(_ context.Context, subject string, limit int, window time.Duration) (allowed, firstRefused bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = map[string]*count{}
	}
	c, ok := l.counts[subject]
	if !ok {
		c = &count{window: window}
		l.counts[subject] = c
	}
	c.n++
	return c.n <= limit, c.n == limit+1, nil
}

// Pass ends every window no longer than d, as if that much time went by.
func (l *Limiter) Pass(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for subject, c := range l.counts {
		if c.window <= d {
			delete(l.counts, subject)
		}
	}
}

// Unlimited admits every attempt, for a test whose subject is not a budget.
type Unlimited struct{}

func (Unlimited) Spend(context.Context, string, int, time.Duration) (bool, bool, error) {
	return true, false, nil
}
