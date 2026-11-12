// Package recoverytest holds in-memory stand-ins for what recovery.Service
// consumes, and a fixture wiring them to a real users.Service, for the tests
// of the domain and of the HTTP layer. Its unit of work rolls the store and
// the outbox back when the work fails, as a transaction would; the accounts
// are not rolled back.
package recoverytest

import (
	"bytes"
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
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
}

// NewFixture builds a service with mail on; edit, when given, changes the
// configuration first.
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
		UnitOfWork: uow,
		PublicURL:  PublicURL,
		Now:        clock.Now,
	}
	if edit != nil {
		edit(&cfg)
	}
	return &Fixture{
		Service: recovery.NewService(cfg), Store: store, Outbox: outbox,
		Users: repo, Hasher: hasher, Audit: trail, Clock: clock,
	}
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
