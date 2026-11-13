// Package recoverytest holds in-memory stand-ins for what recovery.Service
// consumes, and a fixture wiring them to a real users.Service, for the tests
// of the domain and of the HTTP layer. Its unit of work rolls the store and
// the outbox back when the work fails, as a transaction would; the accounts
// are not rolled back.
package recoverytest

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// PublicURL is the address the fixture's links open.
const PublicURL = "https://contest.example.edu"

// StaffRole is a role holding a permission, which makes its holders staff.
const StaffRole = "organizer"

// Fixture is a recovery.Service over in-memory parts, on a settable clock.
type Fixture struct {
	Service *recovery.Service
	Store   *Store
	Outbox  *Outbox
	Users   *userstest.Repository
	Hasher  *Hasher
	Audit   *Audit
	Clock   *Clock
	// Log is what the service logged, at Info and above.
	Log *Log
}

// NewFixture builds a service with mail on; edit, when given, changes the
// configuration first. The service is stopped when the test ends, so no
// request outlives it.
func NewFixture(t testing.TB, edit func(*recovery.Config)) *Fixture {
	t.Helper()
	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	repo.GrantRole(StaffRole, "contest.create")
	clock := &Clock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	store := &Store{}
	outbox := &Outbox{}
	hasher := &Hasher{inner: passwordtest.NewHasher()}
	trail := &Audit{}
	uow := &UnitOfWork{store: store, outbox: outbox}
	log := &Log{}
	logger := slog.New(slog.NewTextHandler(log, nil))
	cfg := recovery.Config{
		Store:     store,
		Directory: repo,
		Accounts:  users.NewService(repo, audit.New(trail), uow, passwordtest.NewHasher()),
		Hasher:    hasher,
		Outbox:    outbox,
		Texts:     mailing.NewTexts(PublicURL),
		Settings: Settings{
			settings.KeyName:         "DB Contest",
			settings.KeyMailLanguage: string(mailing.English),
		},
		MailBudget: mailing.NewBudget("recovery", mailing.Limits{PerMinute: recovery.MailsPerMinute, PerDay: recovery.MailsPerDay},
			auth.NewLimiter(memoryCache(t)), logger),
		UnitOfWork: uow,
		PublicURL:  PublicURL,
		Log:        logger,
		Now:        clock.Now,
	}
	if edit != nil {
		edit(&cfg)
	}
	service := recovery.NewService(cfg)
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	return &Fixture{
		Service: service, Store: store, Outbox: outbox,
		Users: repo, Hasher: hasher, Audit: trail, Clock: clock, Log: log,
	}
}

func memoryCache(t testing.TB) *cache.Memory {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Log is a log destination safe for the service's goroutines.
type Log struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *Log) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// Gate holds whoever reaches it until it is released, telling the test each
// arrival.
type Gate struct {
	entered  chan struct{}
	released chan struct{}
	once     sync.Once
}

func NewGate() *Gate {
	return &Gate{entered: make(chan struct{}, 64), released: make(chan struct{})}
}

// Entered receives once per arrival.
func (g *Gate) Entered() <-chan struct{} { return g.entered }

func (g *Gate) Release() { g.once.Do(func() { close(g.released) }) }

func (g *Gate) pass() {
	g.entered <- struct{}{}
	<-g.released
}

// GatedDirectory is a directory whose lookups by login and email wait at
// Gate, noting the deadline each one was given.
type GatedDirectory struct {
	recovery.Directory
	Gate *Gate

	mu        sync.Mutex
	deadlines []time.Time
}

func (d *GatedDirectory) ByLogin(ctx context.Context, login string) (users.User, error) {
	d.arrive(ctx)
	return d.Directory.ByLogin(ctx, login)
}

func (d *GatedDirectory) ByEmail(ctx context.Context, email string) (users.User, error) {
	d.arrive(ctx)
	return d.Directory.ByEmail(ctx, email)
}

func (d *GatedDirectory) ByID(ctx context.Context, id uuid.UUID) (users.User, error) {
	return d.Directory.ByID(ctx, id)
}

// Deadlines are the deadlines of the lookups so far; a zero time is none.
func (d *GatedDirectory) Deadlines() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.deadlines)
}

func (d *GatedDirectory) arrive(ctx context.Context) {
	deadline, _ := ctx.Deadline()
	d.mu.Lock()
	d.deadlines = append(d.deadlines, deadline)
	d.mu.Unlock()
	d.Gate.pass()
}

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Store keeps links in memory, one per account as the database does, and
// counts the calls made to it.
type Store struct {
	mu    sync.Mutex
	rows  []recovery.Link
	calls int
	// ReplaceErr, when set, is what Replace fails with.
	ReplaceErr error
}

var _ recovery.Store = (*Store)(nil)

func (s *Store) Rows() []recovery.Link {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows)
}

// Calls is how many times the store was asked anything.
func (s *Store) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *Store) Replace(_ context.Context, l recovery.Link) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.ReplaceErr != nil {
		return s.ReplaceErr
	}
	s.rows = slices.DeleteFunc(s.rows, func(r recovery.Link) bool { return r.UserID == l.UserID })
	s.rows = append(s.rows, l)
	return nil
}

func (s *Store) Find(_ context.Context, tokenHash []byte, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return slices.ContainsFunc(s.rows, func(r recovery.Link) bool {
		return bytes.Equal(r.TokenHash, tokenHash) && now.Before(r.ExpiresAt)
	}), nil
}

func (s *Store) Spend(_ context.Context, tokenHash []byte) (recovery.Link, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	for i, r := range s.rows {
		if bytes.Equal(r.TokenHash, tokenHash) {
			s.rows = slices.Delete(s.rows, i, i+1)
			return r, true, nil
		}
	}
	return recovery.Link{}, false, nil
}

func (s *Store) Sweep(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	before := len(s.rows)
	s.rows = slices.DeleteFunc(s.rows, func(r recovery.Link) bool { return !now.Before(r.ExpiresAt) })
	return int64(before - len(s.rows)), nil
}

// Outbox records what was queued; Off turns mail off.
type Outbox struct {
	mu     sync.Mutex
	Off    bool
	queued []mailing.Message
	Wakes  int
}

func (o *Outbox) Enabled() bool { return !o.Off }

func (o *Outbox) EnqueueMany(_ context.Context, ms []mailing.Message) error {
	if o.Off {
		return mailing.ErrDisabled
	}
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queued = append(o.queued, ms...)
	return nil
}

func (o *Outbox) Wake() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Wakes++
}

func (o *Outbox) Queued() []mailing.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.queued)
}

// Hasher is the real hasher, counting what it is asked to hash.
type Hasher struct {
	inner *password.Hasher
	calls atomic.Int64
}

func (h *Hasher) Hash(ctx context.Context, plaintext string) (string, error) {
	h.calls.Add(1)
	return h.inner.Hash(ctx, plaintext)
}

func (h *Hasher) Calls() int64 { return h.calls.Load() }

type Settings settings.Values

func (s Settings) All(context.Context) (settings.Values, error) { return settings.Values(s), nil }

// Audit keeps the entries the accounts' service records.
type Audit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *Audit) Append(_ context.Context, e audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

func (a *Audit) AppendMany(_ context.Context, es []audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, es...)
	return nil
}

func (a *Audit) Entries() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.entries)
}

// UnitOfWork restores the store and the outbox when the work fails.
type UnitOfWork struct {
	store  *Store
	outbox *Outbox
}

func (u *UnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	rows, queued := u.store.Rows(), u.outbox.Queued()
	err := fn(ctx)
	if err != nil {
		u.store.mu.Lock()
		u.store.rows = rows
		u.store.mu.Unlock()
		u.outbox.mu.Lock()
		u.outbox.queued = queued
		u.outbox.mu.Unlock()
	}
	return err
}
