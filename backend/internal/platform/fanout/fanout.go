// Package fanout tells the long-lived streams of this process that something
// they show has changed: a publisher names a topic, and every subscription on
// that topic receives a hint, with no content, to look again.
//
// It is in-process only. With more than one API replica a hint reaches only
// the streams held by the replica that published it; deployments run one
// (deploy/docker-compose.yml), and a client that reconnects reads afresh
// anyway. It does not decide what a topic means or who may subscribe to it.
package fanout

import (
	"sync"

	"github.com/google/uuid"
)

// Hub routes hints from publishers to subscriptions. The zero Hub is not
// usable; use NewHub.
type Hub struct {
	mu     sync.RWMutex
	topics map[uuid.UUID]map[*subscription]struct{}
}

type subscription struct {
	hints chan struct{}
}

func NewHub() *Hub {
	return &Hub{topics: map[uuid.UUID]map[*subscription]struct{}{}}
}

// Subscribe returns a channel that receives a hint whenever any of topics is
// published, and the function that ends the subscription. Hints coalesce: an
// unread hint absorbs every later one, so a slow reader costs a publisher
// nothing. Close may be called more than once.
func (h *Hub) Subscribe(topics ...uuid.UUID) (<-chan struct{}, func()) {
	s := &subscription{hints: make(chan struct{}, 1)}
	h.mu.Lock()
	for _, topic := range topics {
		if h.topics[topic] == nil {
			h.topics[topic] = map[*subscription]struct{}{}
		}
		h.topics[topic][s] = struct{}{}
	}
	h.mu.Unlock()

	var once sync.Once
	return s.hints, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			for _, topic := range topics {
				delete(h.topics[topic], s)
				if len(h.topics[topic]) == 0 {
					delete(h.topics, topic)
				}
			}
		})
	}
}

// Publish hints every subscription on topic. It never blocks: one publish to
// a contest of five hundred open streams is five hundred non-blocking sends.
func (h *Hub) Publish(topic uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.topics[topic] {
		select {
		case s.hints <- struct{}{}:
		default:
		}
	}
}

// Subscribers reports how many subscriptions topic has, so a test can see
// that closed ones are gone.
func (h *Hub) Subscribers(topic uuid.UUID) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.topics[topic])
}
