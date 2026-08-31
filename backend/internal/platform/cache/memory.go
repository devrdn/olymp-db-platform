package cache

import (
	"container/list"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"
)

// ErrFull reports a store that has no room for a new counter.
//
// Only counters see it. A value (a session) makes room by evicting the least
// recently used one, because losing a session costs a re-login. A counter is
// the brute-force limit itself: evicting one would let an attacker reset a
// victim's window simply by attempting logins against many other names, and
// nothing would record that it had happened. Limiter.Allow reads an error as
// "the protection is not in place" and refuses, which turns a silent bypass
// into a visible refusal.
var ErrFull = errors.New("cache is full")

// defaultCapacity bounds the in-process store. Keys are derived from user
// input (session ids, rate-limit subjects), so an unbounded map would be a way
// to grow the process until the host kills it.
const defaultCapacity = 100_000

// Memory is an in-process cache with per-entry TTL and least-recently-used
// eviction.
//
// It is the fallback backend: correct for one instance, wrong for several,
// because nothing is shared between them.
type Memory struct {
	capacity int

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // front = most recently used
}

type entry struct {
	key       string
	value     []byte
	expiresAt time.Time
}

// NewMemory returns an in-process cache holding at most capacity entries.
func NewMemory(capacity int) *Memory {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Memory{
		capacity: capacity,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// Get returns the value if present and not expired.
func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	el, ok := m.entries[key]
	if !ok {
		return nil, false, nil
	}

	e := el.Value.(*entry)
	if time.Now().After(e.expiresAt) {
		m.removeElement(el)
		return nil, false, nil
	}

	m.order.MoveToFront(el)

	// Copy: the caller must not be able to mutate what is still stored.
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true, nil
}

// Set stores a value for ttl.
func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored := make([]byte, len(value))
	copy(stored, value)

	m.set(key, stored, ttl)
	return nil
}

// Delete removes a key, whether or not it was present.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.entries[key]; ok {
		m.removeElement(el)
	}
	return nil
}

// Incr increments a counter, starting a fresh window when the key is absent or
// expired. The TTL is set once per window and not extended by later increments,
// which is what makes it a fixed rate-limit window rather than a sliding one
// that never resets under continuous load.
func (m *Memory) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.entries[key]; ok {
		e := el.Value.(*entry)
		if time.Now().Before(e.expiresAt) {
			n, err := strconv.ParseInt(string(e.value), 10, 64)
			if err != nil {
				return 0, err
			}
			n++
			e.value = []byte(strconv.FormatInt(n, 10))
			m.order.MoveToFront(el)
			return n, nil
		}
		m.removeElement(el)
	}

	// A new window needs a slot of its own, and it may not take one from a
	// counter that is still running. Windows that have already passed are not
	// holding their place on merit, so they are reclaimed first.
	if len(m.entries) >= m.capacity {
		m.reclaimExpired()
		if len(m.entries) >= m.capacity {
			return 0, ErrFull
		}
	}

	m.set(key, []byte("1"), ttl)
	return 1, nil
}

// reclaimExpired drops entries whose time has passed, oldest first. Callers
// hold the lock.
//
// It walks from the back, where the least recently used sit, and stops at the
// first live one: the list is ordered by use rather than by expiry, so a full
// scan would cost the whole store on every full write, and the entries most
// likely to have lapsed are the ones nobody has touched.
func (m *Memory) reclaimExpired() {
	now := time.Now()

	for el := m.order.Back(); el != nil; {
		previous := el.Prev()
		if now.After(el.Value.(*entry).expiresAt) {
			m.removeElement(el)
		}
		el = previous
	}
}

// Ping always succeeds: the store is this process.
func (m *Memory) Ping(context.Context) error { return nil }

// Close releases the entries.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = map[string]*list.Element{}
	m.order.Init()
	return nil
}

// Len reports the number of stored entries, including expired ones that have
// not been reclaimed yet.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// set writes an entry and enforces the capacity bound. Callers hold the lock.
func (m *Memory) set(key string, value []byte, ttl time.Duration) {
	if el, ok := m.entries[key]; ok {
		e := el.Value.(*entry)
		e.value = value
		e.expiresAt = time.Now().Add(ttl)
		m.order.MoveToFront(el)
		return
	}

	el := m.order.PushFront(&entry{
		key:       key,
		value:     value,
		expiresAt: time.Now().Add(ttl),
	})
	m.entries[key] = el

	for len(m.entries) > m.capacity {
		if oldest := m.order.Back(); oldest != nil {
			m.removeElement(oldest)
		}
	}
}

// removeElement drops an entry. Callers hold the lock.
func (m *Memory) removeElement(el *list.Element) {
	m.order.Remove(el)
	delete(m.entries, el.Value.(*entry).key)
}
