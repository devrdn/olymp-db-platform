package clarificationstest

import (
	"context"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/clarifications"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// FixtureNow is the time the fixture's clock starts at.
var FixtureNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// Fixture is the real clarifications.Service over in-memory fakes.
type Fixture struct {
	Service  *clarifications.Service
	Repo     *Repository
	Contests *Contests
	Limiter  *Limiter
	Notifier *Notifier
	Audit    *Sink
	UoW      *UnitOfWork
	// Now is what the service's clock reads; a test moves it.
	Now time.Time
}

func NewFixture() *Fixture {
	f := &Fixture{
		Repo: NewRepository(), Contests: NewContests(), Limiter: NewLimiter(), Notifier: &Notifier{},
		Audit: &Sink{}, UoW: &UnitOfWork{}, Now: FixtureNow,
	}
	f.Service = clarifications.NewService(clarifications.Config{
		Repository: f.Repo, Contests: f.Contests, Limiter: f.Limiter, Notifier: f.Notifier,
		Audit: audit.New(f.Audit), UnitOfWork: f.UoW,
		Now: func() time.Time { return f.Now },
	})
	return f
}

// Tick moves the clock on, so the next write sorts after the last.
func (f *Fixture) Tick() { f.Now = f.Now.Add(time.Second) }

// RunningContest stores a running contest.
func (f *Fixture) RunningContest() uuid.UUID {
	id := uuid.New()
	f.Contests.Set(contests.Contest{ID: id, Status: contests.StatusRunning})
	return id
}

// Join registers a new participant in contest and returns the registration.
func (f *Fixture) Join(contest uuid.UUID, login string) uuid.UUID {
	id := uuid.New()
	f.Repo.Register(contest, id, login, "Full "+login)
	return id
}

// Question adds a question to contest.
func (f *Fixture) Question(contest uuid.UUID) uuid.UUID {
	id := uuid.New()
	f.Repo.AddQuestion(contest, id)
	return id
}

// Contests is clarifications.Contests in memory.
type Contests struct {
	mu   sync.Mutex
	byID map[uuid.UUID]contests.Contest
}

func NewContests() *Contests { return &Contests{byID: map[uuid.UUID]contests.Contest{}} }

func (c *Contests) Set(contest contests.Contest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[contest.ID] = contest
}

// SetStatus moves a stored contest to status.
func (c *Contests) SetStatus(id uuid.UUID, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	contest := c.byID[id]
	contest.Status = status
	c.byID[id] = contest
}

func (c *Contests) ByID(_ context.Context, id uuid.UUID) (contests.Contest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	contest, ok := c.byID[id]
	if !ok {
		return contests.Contest{}, contests.ErrNotFound
	}
	return contest, nil
}

// Limiter counts every call per subject and allows while the count is within
// the limit, ignoring the window: a test runs inside one.
type Limiter struct {
	mu    sync.Mutex
	calls map[string]int
	// Err, when set, is what every call returns.
	Err error
}

func NewLimiter() *Limiter { return &Limiter{calls: map[string]int{}} }

func (l *Limiter) Allow(_ context.Context, subject string, limit int, _ time.Duration) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Err != nil {
		return false, l.Err
	}
	l.calls[subject]++
	return l.calls[subject] <= limit, nil
}

// Calls is how often subject was charged, refusals included.
func (l *Limiter) Calls(subject string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[subject]
}

// Reset starts every subject's window afresh.
func (l *Limiter) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = map[string]int{}
}

// Notifier records every topic published, in order.
type Notifier struct {
	mu     sync.Mutex
	topics []uuid.UUID
}

func (n *Notifier) Publish(topic uuid.UUID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.topics = append(n.topics, topic)
}

// Topics returns what was published and forgets it.
func (n *Notifier) Topics() []uuid.UUID {
	n.mu.Lock()
	defer n.mu.Unlock()
	topics := n.topics
	n.topics = nil
	return topics
}

// Sink collects audit entries, noting which were written with no transaction
// open.
type Sink struct {
	mu      sync.Mutex
	Entries []audit.Entry
	Loose   []audit.Entry
}

func (s *Sink) Append(ctx context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Entries = append(s.Entries, e)
	if !inTx(ctx) {
		s.Loose = append(s.Loose, e)
	}
	return nil
}

func (s *Sink) AppendMany(ctx context.Context, entries []audit.Entry) error {
	for _, e := range entries {
		if err := s.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// UnitOfWork runs fn directly and marks the context as inside a transaction.
// It cannot roll back the fakes; atomicity is the real runner's to prove.
type UnitOfWork struct {
	Calls int
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	u.Calls++
	return fn(context.WithValue(ctx, txKey{}, true))
}

type txKey struct{}

func inTx(ctx context.Context) bool {
	open, _ := ctx.Value(txKey{}).(bool)
	return open
}
