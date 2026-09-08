package gamefile

import "sync"

// idLocks serialises Store's mutating calls — Append, Complete, Abort — per
// upload id, so that "Append writes exactly at the end" and "Complete's
// checksum describes what is on disk" are guarantees Store keeps itself,
// not properties that happen to hold only because a caller sends one chunk
// at a time. A browser resending a chunk it never got a response for is a
// second goroutine reaching Append for the same id while the first attempt
// may still be writing — a real case, not a caller bug — and this is what
// keeps the two from reading the same length, seeking to the same offset,
// and overwriting each other.
//
// This is deliberately not one mutex for the whole Store: uploads are
// independent of one another, and a contest with several concurrent
// uploads must not have one of them wait on another's disk I/O just because
// they happen to share a Store. Locking is per id.
//
// Entries are reference counted and removed the instant nothing holds them,
// so this map is bounded by uploads currently in flight — never by every id
// a long-lived process has ever seen.
type idLocks struct {
	mu   sync.Mutex
	byID map[string]*idLock
}

// idLock is one upload id's mutex plus how many goroutines currently hold a
// reference to it, so the entry can be removed from idLocks.byID the moment
// the last one releases it.
type idLock struct {
	mu   sync.Mutex
	refs int
}

func newIDLocks() *idLocks {
	return &idLocks{byID: make(map[string]*idLock)}
}

// lock acquires the per-id lock for id, blocking until it is available, and
// returns a function that releases it. Callers must arrange for the
// returned function to run exactly once, typically via defer immediately
// after calling lock.
func (l *idLocks) lock(id string) func() {
	l.mu.Lock()
	lk, ok := l.byID[id]
	if !ok {
		lk = &idLock{}
		l.byID[id] = lk
	}
	lk.refs++
	l.mu.Unlock()

	lk.mu.Lock()

	return func() {
		lk.mu.Unlock()

		l.mu.Lock()
		lk.refs--
		if lk.refs == 0 {
			// Nobody else is waiting on lk (a waiter would already have
			// incremented refs before we could observe zero here, since
			// every increment and this decrement both happen under l.mu).
			// Safe to drop it so the map does not grow forever.
			delete(l.byID, id)
		}
		l.mu.Unlock()
	}
}
