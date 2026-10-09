package gamefile

import "sync"

// idLocks serialises Append, Complete and Abort per upload id. A browser
// retrying a chunk it got no response for can reach Append while the first
// attempt is still writing; without the lock both would write at the same
// offset. Per id rather than per Store, so independent uploads never wait on
// each other's disk I/O.
//
// Entries are reference counted and removed when unused, so the map is bounded
// by uploads in flight.
type idLocks struct {
	mu   sync.Mutex
	byID map[string]*idLock
}

type idLock struct {
	mu   sync.Mutex
	refs int
}

func newIDLocks() *idLocks {
	return &idLocks{byID: make(map[string]*idLock)}
}

// lock blocks until id's lock is held and returns its release, which must run
// exactly once.
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
			// No waiter exists: every waiter increments refs under l.mu
			// before waiting.
			delete(l.byID, id)
		}
		l.mu.Unlock()
	}
}
