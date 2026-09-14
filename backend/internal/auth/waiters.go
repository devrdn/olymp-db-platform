package auth

import "sync"

// maxWaitingAddresses bounds how many addresses the waiting counts track at
// once. Each entry stands for at least one request blocked on a hashing slot,
// so reaching it takes that many concurrent sign-ins from distinct addresses.
const maxWaitingAddresses = 4096

// waitingQueue caps how many sign-in attempts from one address may wait for a
// hashing slot at the same time.
//
// The hasher's queue is first come, first served, and before an attempt joins
// it the attempt has paid only its address budget, which is hundreds a window
// so that a lecture hall behind one NAT address is not refused. That budget
// bounds how many attempts an address makes, not how many it makes at once:
// hundreds arriving together would sit in front of a handful of slots, and
// every other sign-in would outlast its wait behind them. A cap on each
// address's share of the queue keeps the queue short enough for everybody
// else to be served within the wait.
//
// It is a fairness bound, not the memory bound — the hasher's slots are that —
// so when the table of addresses is full it lets an attempt through
// uncounted rather than refusing every address it has not seen: a full table
// already means thousands of concurrent sign-ins from distinct addresses, a
// load the cap per address could not have prevented anyway.
type waitingQueue struct {
	limit int

	mu      sync.Mutex
	waiting map[string]int
}

func newWaitingQueue(limit int) *waitingQueue {
	return &waitingQueue{limit: limit, waiting: make(map[string]int)}
}

// join admits one more waiting attempt for subject. It reports false when the
// subject already has its limit waiting; otherwise the caller must call the
// returned leave exactly once when it stops waiting.
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
	// An address with nobody waiting holds no entry, so the table is only as
	// large as the set of addresses waiting right now.
	if q.waiting[subject] <= 1 {
		delete(q.waiting, subject)
		return
	}
	q.waiting[subject]--
}
