package mailing_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/google/uuid"
)

type fakeQueue struct {
	inserted []mailing.Message
	// batches counts InsertMany calls.
	batches   int
	abandoned []abandonCall
	err       error
}

type abandonCall struct {
	kind   mailing.Kind
	users  []uuid.UUID
	reason string
}

func (q *fakeQueue) Insert(_ context.Context, m mailing.Message) error {
	if q.err != nil {
		return q.err
	}
	q.inserted = append(q.inserted, m)
	return nil
}

func (q *fakeQueue) InsertMany(_ context.Context, ms []mailing.Message) error {
	if q.err != nil {
		return q.err
	}
	q.batches++
	q.inserted = append(q.inserted, ms...)
	return nil
}

func (q *fakeQueue) AbandonPending(_ context.Context, kind mailing.Kind, users []uuid.UUID, reason string) error {
	if q.err != nil {
		return q.err
	}
	q.abandoned = append(q.abandoned, abandonCall{kind: kind, users: users, reason: reason})
	return nil
}

// fakeStore hands out its due messages in the repository's claim order,
// (next_attempt_at, id), each claim counting an attempt; a retried or released
// message goes back with its new due time. It records what the dispatcher did.
type fakeStore struct {
	mu      sync.Mutex
	pending []mailing.Queued
	// dueAt is a pending message's next_attempt_at; absent is due from the
	// start. clock is the dispatcher's, set by dispatcherAt.
	dueAt   map[int64]time.Time
	clock   func() time.Time
	claimed map[int64]mailing.Queued

	claimedPerMinute []int
	claimedLease     []time.Duration
	sent             []int64
	retried          []retry
	released         []retry
	abandoned        []abandon
	due              int

	// expired is what the next Expire reports; expiredAsked records its
	// arguments.
	expired      map[mailing.Kind]int
	expiredAsked []expireCall

	claimErr error
	// claimLog is every claimed id, in order.
	claimLog []int64
	// recordCtxErr is the error of the context each record call received.
	recordCtxErr []error
}

type retry struct {
	id        int64
	after     time.Duration
	lastError string
}

type expireCall struct {
	maxAge, lease time.Duration
	reason        string
}

type abandon struct {
	id        int64
	lastError string
	attempts  int
}

func (s *fakeStore) Claim(ctx context.Context, perMinute int, lease time.Duration) (mailing.Queued, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimedPerMinute = append(s.claimedPerMinute, perMinute)
	s.claimedLease = append(s.claimedLease, lease)
	if s.claimErr != nil {
		return mailing.Queued{}, false, s.claimErr
	}
	next := -1
	for i, q := range s.pending {
		if !s.isDue(q.ID) {
			continue
		}
		if next < 0 || s.dueAt[q.ID].Before(s.dueAt[s.pending[next].ID]) ||
			(s.dueAt[q.ID].Equal(s.dueAt[s.pending[next].ID]) && q.ID < s.pending[next].ID) {
			next = i
		}
	}
	if next < 0 {
		return mailing.Queued{}, false, nil
	}
	q := s.pending[next]
	s.pending = append(s.pending[:next:next], s.pending[next+1:]...)
	q.Attempts++
	s.claimLog = append(s.claimLog, q.ID)
	if s.claimed == nil {
		s.claimed = map[int64]mailing.Queued{}
	}
	s.claimed[q.ID] = q
	return q, true, nil
}

func (s *fakeStore) now() time.Time {
	if s.clock == nil {
		return now
	}
	return s.clock()
}

func (s *fakeStore) isDue(id int64) bool {
	due, ok := s.dueAt[id]
	return !ok || !due.After(s.now())
}

// putBack returns a claimed message to the queue, due after the given wait.
func (s *fakeStore) putBack(id int64, after time.Duration, refund bool) {
	q := s.claimed[id]
	delete(s.claimed, id)
	if refund {
		q.Attempts--
	}
	if s.dueAt == nil {
		s.dueAt = map[int64]time.Time{}
	}
	s.dueAt[id] = s.now().Add(after)
	s.pending = append(s.pending, q)
}

// attemptsOf is the attempts a pending message has been charged.
func (s *fakeStore) attemptsOf(id int64) int {
	for _, q := range s.pending {
		if q.ID == id {
			return q.Attempts
		}
	}
	return -1
}

func (s *fakeStore) MarkSent(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.sent = append(s.sent, id)
	delete(s.claimed, id)
	return nil
}

func (s *fakeStore) Retry(ctx context.Context, id int64, after time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.retried = append(s.retried, retry{id, after, lastError})
	s.putBack(id, after, false)
	return nil
}

func (s *fakeStore) Release(ctx context.Context, id int64, after time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.released = append(s.released, retry{id, after, lastError})
	s.putBack(id, after, true)
	return nil
}

func (s *fakeStore) Expire(ctx context.Context, maxAge, lease time.Duration, reason string) (map[mailing.Kind]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiredAsked = append(s.expiredAsked, expireCall{maxAge, lease, reason})
	expired := s.expired
	s.expired = nil
	return expired, nil
}

func (s *fakeStore) Abandon(ctx context.Context, id int64, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.abandoned = append(s.abandoned, abandon{id, lastError, s.claimed[id].Attempts})
	delete(s.claimed, id)
	return nil
}

func (s *fakeStore) CountDue(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.due, nil
}

// fakeTransport answers a recipient in byRecipient with its error, and
// anyone else with the next error in replies (nil when they run out), and
// records what it was asked to send.
type fakeTransport struct {
	byRecipient map[string]error
	replies     []error
	sent        []sentMessage
	// during runs inside Send, before it answers.
	during func()
}

type sentMessage struct{ to, subject, body string }

func (f *fakeTransport) Send(_ context.Context, to, subject, body string) error {
	f.sent = append(f.sent, sentMessage{to, subject, body})
	if f.during != nil {
		f.during()
	}
	if err, ok := f.byRecipient[to]; ok {
		return err
	}
	if len(f.replies) == 0 {
		return nil
	}
	err := f.replies[0]
	f.replies = f.replies[1:]
	return err
}

type fakeCounters struct {
	counted       map[string]int
	due           int
	relayFailures int
	relayFailing  bool
}

func (c *fakeCounters) RelayFailed()                 { c.relayFailures++ }
func (c *fakeCounters) SetRelayFailing(failing bool) { c.relayFailing = failing }

func (c *fakeCounters) Count(kind, outcome string) {
	if c.counted == nil {
		c.counted = map[string]int{}
	}
	c.counted[kind+"/"+outcome]++
}

func (c *fakeCounters) SetDue(n int) { c.due = n }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// queued is a message the store holds, created at created and tried attempts
// times before.
func queued(id int64, created time.Time, attempts int) mailing.Queued {
	return mailing.Queued{ID: id, Message: aMessage(), Attempts: attempts, CreatedAt: created}
}
