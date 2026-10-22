package mailing_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
)

type fakeQueue struct {
	inserted []mailing.Message
	err      error
}

func (q *fakeQueue) Insert(_ context.Context, m mailing.Message) error {
	if q.err != nil {
		return q.err
	}
	q.inserted = append(q.inserted, m)
	return nil
}

// fakeStore hands out its pending messages in order, each claim counting an
// attempt the way the repository does, and records what the dispatcher did.
type fakeStore struct {
	mu      sync.Mutex
	pending []mailing.Queued

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

	claimErr    error
	claimedLast mailing.Queued
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
}

func (s *fakeStore) Claim(ctx context.Context, perMinute int, lease time.Duration) (mailing.Queued, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimedPerMinute = append(s.claimedPerMinute, perMinute)
	s.claimedLease = append(s.claimedLease, lease)
	if s.claimErr != nil {
		return mailing.Queued{}, false, s.claimErr
	}
	if len(s.pending) == 0 {
		return mailing.Queued{}, false, nil
	}
	q := s.pending[0]
	s.pending = s.pending[1:]
	q.Attempts++
	s.claimedLast = q
	return q, true, nil
}

func (s *fakeStore) MarkSent(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.sent = append(s.sent, id)
	return nil
}

func (s *fakeStore) Retry(ctx context.Context, id int64, after time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.retried = append(s.retried, retry{id, after, lastError})
	return nil
}

// Release puts the message back as the next one due, its attempt refunded.
func (s *fakeStore) Release(ctx context.Context, id int64, after time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = append(s.recordCtxErr, ctx.Err())
	s.released = append(s.released, retry{id, after, lastError})
	s.pending = append([]mailing.Queued{s.claimedLast}, s.pending...)
	s.pending[0].Attempts--
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
	s.abandoned = append(s.abandoned, abandon{id, lastError})
	return nil
}

func (s *fakeStore) CountDue(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.due, nil
}

// fakeTransport answers each Send with the next error in replies (nil when
// they run out) and records what it was asked to send.
type fakeTransport struct {
	replies []error
	sent    []sentMessage
	// during runs inside Send, before it answers.
	during func()
}

type sentMessage struct{ to, subject, body string }

func (f *fakeTransport) Send(_ context.Context, to, subject, body string) error {
	f.sent = append(f.sent, sentMessage{to, subject, body})
	if f.during != nil {
		f.during()
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
