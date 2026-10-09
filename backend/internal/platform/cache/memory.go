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
// Only counters see it. A value (a session) evicts the least recently used
// one, since losing a session costs a re-login. Evicting a counter would let
// an attacker reset a victim's brute-force window by trying many other names;
// Limiter.Allow refuses on this error instead, so the bypass becomes a
// visible refusal.
var ErrFull = errors.New("cache is full")

// defaultCapacity bounds the in-process store, whose keys derive from user
// input.
const defaultCapacity = 100_000

// Memory is an in-process cache with per-entry TTL and least-recently-used
// eviction. It is the fallback backend: correct for one instance only.
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

	// Copy, so the caller cannot mutate what is stored.
	out := make([]byte, len(e.value))
	copy(out, e.value)
	return out, true, nil
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored := make([]byte, len(value))
	copy(stored, value)

	m.set(key, stored, ttl)
	return nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if el, ok := m.entries[key]; ok {
		m.removeElement(el)
	}
	return nil
}

// Incr increments a counter, starting a fresh window when the key is absent or
// expired. The TTL is set once per window and not extended, so the window is
// fixed rather than sliding and resets even under continuous load.
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

	// A new window may not evict a running counter; expired ones are
	// reclaimed first.
	if len(m.entries) >= m.capacity {
		m.reclaimExpired()
		if len(m.entries) >= m.capacity {
			return 0, ErrFull
		}
	}

	m.set(key, []byte("1"), ttl)
	return 1, nil
}

// reclaimScan bounds how many entries one reclaim examines. The walk runs
// under the lock every session read and rate-limit check takes, so a walk of
// the whole store would turn one caller's flood into everybody's stall. The
// untouched windows sit in the tail, so a short walk keeps refusals rare.
const reclaimScan = 64

// reclaimExpired drops lapsed entries among the reclaimScan least recently
// used. It does not stop at the first live one: the list is ordered by use,
// not expiry. Callers hold the lock.
func (m *Memory) reclaimExpired() {
	now := time.Now()

	el := m.order.Back()
	for examined := 0; el != nil && examined < reclaimScan; examined++ {
		previous := el.Prev()
		if now.After(el.Value.(*entry).expiresAt) {
			m.removeElement(el)
		}
		el = previous
	}
}

// Ping always succeeds: the store is this process.
func (m *Memory) Ping(context.Context) error { return nil }

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
