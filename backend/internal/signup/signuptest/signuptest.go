// Package signuptest holds in-memory stand-ins for what signup.Service
// consumes, and a fixture wiring them to a real users.Service, for the tests
// of the domain and of the HTTP layer. Its unit of work rolls the store and
// the outbox back when the work fails, as a transaction would; the accounts
// are not rolled back.
package signuptest

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/mailing"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/password"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/olymp-db-platform/backend/internal/settings"
	"github.com/devrdn/olymp-db-platform/backend/internal/signup"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// Fixture is a signup.Service over in-memory parts, on a settable clock.
type Fixture struct {
	Service *signup.Service
	Store   *Store
	Outbox  *Outbox
	Users   *userstest.Repository
	Hasher  *Hasher
	Clock   *Clock
}

// NewFixture builds a service with sign-up and mail on; edit, when given,
// changes the configuration first.
func NewFixture(t testing.TB, edit func(*signup.Config)) *Fixture {
	t.Helper()
	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	clock := &Clock{now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	store := &Store{}
	outbox := &Outbox{}
	hasher := &Hasher{inner: passwordtest.NewHasher()}
	cfg := signup.Config{
		Enabled:   true,
		Store:     store,
		Directory: repo,
		Accounts:  users.NewService(repo, audit.New(discard{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()),
		Hasher:    hasher,
		Outbox:    outbox,
		Texts:     mailing.NewTexts("https://contest.example.edu"),
		Settings: Settings{
			settings.KeyName:         "DB Contest",
			settings.KeyMailLanguage: string(mailing.English),
		},
		Limiter: auth.NewLimiter(memoryCache(t)),
		MailBudget: mailing.NewBudget("signup", mailing.Limits{PerMinute: signup.MailsPerMinute, PerDay: signup.MailsPerDay},
			auth.NewLimiter(memoryCache(t)), slog.New(slog.NewTextHandler(io.Discard, nil))),
		UnitOfWork: &UnitOfWork{store: store, outbox: outbox},
		Now:        clock.Now,
	}
	if edit != nil {
		edit(&cfg)
	}
	return &Fixture{
		Service: signup.NewService(cfg), Store: store, Outbox: outbox,
		Users: repo, Hasher: hasher, Clock: clock,
	}
}

func memoryCache(t testing.TB) *cache.Memory {
	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	return c
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

// Store keeps pending sign-ups in memory, with the database's rules.
type Store struct {
	mu   sync.Mutex
	rows []signup.Pending
}

var _ signup.Store = (*Store)(nil)

func (s *Store) Rows() []signup.Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows)
}

// Fill stores n sign-ups of made-up addresses expiring at expiresAt.
func (s *Store) Fill(n int, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		id := uuid.New()
		s.rows = append(s.rows, signup.Pending{
			ID: id, Email: id.String() + "@example.edu", Login: id.String(), FullName: "Filler",
			PasswordHash: "x", CodeHash: []byte("x"), SentAt: expiresAt.Add(-signup.CodeLifetime), ExpiresAt: expiresAt,
		})
	}
}

func (s *Store) Waiting(_ context.Context, email string, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.rows {
		if p.Email != email && now.Before(p.ExpiresAt) {
			n++
		}
	}
	return n, nil
}

func (s *Store) LoginHeld(_ context.Context, login, email string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldLocked(login, email, now), nil
}

func (s *Store) heldLocked(login, email string, now time.Time) bool {
	return slices.ContainsFunc(s.rows, func(p signup.Pending) bool {
		return p.Email != email && strings.EqualFold(p.Login, login) && now.Before(p.ExpiresAt)
	})
}

func (s *Store) Put(_ context.Context, p signup.Pending, max int, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = slices.DeleteFunc(s.rows, func(r signup.Pending) bool {
		return r.Email != p.Email && strings.EqualFold(r.Login, p.Login) && !now.Before(r.ExpiresAt)
	})
	if s.heldLocked(p.Login, p.Email, now) {
		return users.ErrLoginTaken
	}
	waiting := 0
	for _, r := range s.rows {
		if r.Email != p.Email && now.Before(r.ExpiresAt) {
			waiting++
		}
	}
	if waiting >= max {
		return signup.ErrFull
	}
	p.Tries = 0
	for i, r := range s.rows {
		if r.Email == p.Email {
			p.ID = r.ID
			s.rows[i] = p
			return nil
		}
	}
	p.ID = uuid.New()
	s.rows = append(s.rows, p)
	return nil
}

func (s *Store) Take(_ context.Context, email string) (signup.Pending, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.rows {
		if p.Email == email {
			return p, true, nil
		}
	}
	return signup.Pending{}, false, nil
}

func (s *Store) CountTry(_ context.Context, id uuid.UUID) error {
	return s.update(id, func(p *signup.Pending) { p.Tries++ })
}

func (s *Store) Renew(_ context.Context, id uuid.UUID, codeHash []byte, sentAt, expiresAt time.Time) error {
	return s.update(id, func(p *signup.Pending) {
		p.CodeHash, p.SentAt, p.ExpiresAt = slices.Clone(codeHash), sentAt, expiresAt
	})
}

func (s *Store) Delete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = slices.DeleteFunc(s.rows, func(p signup.Pending) bool { return p.ID == id })
	return nil
}

func (s *Store) Sweep(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := len(s.rows)
	s.rows = slices.DeleteFunc(s.rows, func(p signup.Pending) bool { return !now.Before(p.ExpiresAt) })
	return int64(before - len(s.rows)), nil
}

func (s *Store) update(id uuid.UUID, fn func(*signup.Pending)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID == id {
			fn(&s.rows[i])
		}
	}
	return nil
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

// Hasher is the real hasher, counting what it is asked to hash. During, when
// set, runs inside each hash, as time passing while it works.
type Hasher struct {
	inner  *password.Hasher
	calls  atomic.Int64
	During func()
}

func (h *Hasher) Hash(ctx context.Context, plaintext string) (string, error) {
	h.calls.Add(1)
	if h.During != nil {
		h.During()
	}
	return h.inner.Hash(ctx, plaintext)
}

func (h *Hasher) Calls() int64 { return h.calls.Load() }

type Settings settings.Values

func (s Settings) All(context.Context) (settings.Values, error) { return settings.Values(s), nil }

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

type discard struct{}

func (discard) Append(context.Context, audit.Entry) error       { return nil }
func (discard) AppendMany(context.Context, []audit.Entry) error { return nil }
