package auth

import "sync"

// maxWaitingAddresses bounds how many addresses are tracked at once; each
// entry is at least one request blocked on a hashing slot.
const maxWaitingAddresses = 4096

// waitingQueue caps how many sign-in attempts from one address may wait for a
// hashing slot at once. The address budget bounds attempts per window, not at
// once, so without this one address could fill the queue and starve everyone
// else past their wait.
//
// It is a fairness bound, not the memory bound, so a full address table lets
// an attempt through uncounted rather than refusing unseen addresses.
type waitingQueue struct {
	limit int

	mu      sync.Mutex
	waiting map[string]int
}

func newWaitingQueue(limit int) *waitingQueue {
	return &waitingQueue{limit: limit, waiting: make(map[string]int)}
}

// join admits one more waiting attempt for subject, or reports false at its
// limit. Otherwise the caller must call leave exactly once.
func (q *waitingQueue) join(subject string) (leave func(), ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	count, tracked := q.waiting[subject]
	if !tracked && len(q.waiting) >= maxWaitingAddresses {
		return func() {}, true
	}
	if count >= q.limit {
		return nil, false
	}
	q.waiting[subject] = count + 1

	var once sync.Once
	return func() { once.Do(func() { q.leave(subject) }) }, true
}

func (q *waitingQueue) leave(subject string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// An address with nobody waiting holds no entry.
	if q.waiting[subject] <= 1 {
		delete(q.waiting, subject)
		return
	}
	q.waiting[subject]--
}
