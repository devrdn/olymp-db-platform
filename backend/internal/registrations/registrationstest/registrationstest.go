// Package registrationstest holds in-memory stand-ins for what
// registrations.Service consumes, for the tests of the domain and of the HTTP
// layer. It does not model transactions: a failed unit of work leaves
// whatever the store already holds.
package registrationstest

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

// Store keeps registrations in memory, with the cooldown measured on Now.
type Store struct {
	mu   sync.Mutex
	rows []registrations.Registration
	// Logins names the accounts registrations link to, as the database's
	// join would.
	Logins map[uuid.UUID]string
	now    time.Time
	// Writes counts the calls that would reach the database.
	Writes int
	// BeforeInsert, when set, runs as Insert begins, without the store's
	// lock, so a test can store a competing registration.
	BeforeInsert func(r registrations.Registration)
}

var _ registrations.Store = (*Store)(nil)

func NewStore() *Store {
	return &Store{Logins: map[uuid.UUID]string{}, now: time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)}
}

// Advance moves the store's clock.
func (s *Store) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
}

// Fill stores n registrations of made-up addresses.
func (s *Store) Fill(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for range n {
		id := uuid.New()
		s.rows = append(s.rows, registrations.Registration{
			ID: id, FirstName: "Filler", LastName: "Row", Email: id.String() + "@example.com",
			EmailKey: id.String() + "@example.com", Code: id.String()[:6], CreatedAt: s.now,
		})
	}
}

// Put stores r as given, with a fresh id.
func (s *Store) Put(r registrations.Registration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = uuid.New()
	r.CreatedAt = s.now
	s.rows = append(s.rows, r)
}

func (s *Store) Rows() []registrations.Registration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows)
}

func (s *Store) ByEmailKey(_ context.Context, key string) (registrations.Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	for _, r := range s.rows {
		if r.EmailKey == key {
			return s.withLogin(r), nil
		}
	}
	return registrations.Registration{}, registrations.ErrNotFound
}

func (s *Store) Count(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	return len(s.rows), nil
}

func (s *Store) Insert(_ context.Context, r registrations.Registration, capacity int, sent bool) (registrations.Registration, error) {
	if s.BeforeInsert != nil {
		s.BeforeInsert(r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	for _, existing := range s.rows {
		if existing.EmailKey == r.EmailKey {
			return registrations.Registration{}, registrations.ErrAlreadyRegistered
		}
	}
	if len(s.rows) >= capacity {
		return registrations.Registration{}, registrations.ErrFull
	}
	for _, existing := range s.rows {
		if existing.Code == r.Code {
			return registrations.Registration{}, registrations.ErrCodeTaken
		}
	}
	r.ID = uuid.New()
	r.CreatedAt = s.now
	if sent {
		at := s.now
		r.LastSentAt = &at
	}
	s.rows = append(s.rows, r)
	return s.withLogin(r), nil
}

func (s *Store) AttachAccount(_ context.Context, emailKey string, userID uuid.UUID) (registrations.Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	for i, r := range s.rows {
		if r.EmailKey == emailKey && r.UserID == nil {
			s.rows[i].UserID = &userID
			return s.withLogin(s.rows[i]), nil
		}
	}
	return registrations.Registration{}, registrations.ErrNotFound
}

func (s *Store) ClaimResend(_ context.Context, id uuid.UUID, cooldown time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	for i, r := range s.rows {
		if r.ID != id {
			continue
		}
		if r.LastSentAt != nil && s.now.Sub(*r.LastSentAt) < cooldown {
			return false, nil
		}
		at := s.now
		s.rows[i].LastSentAt = &at
		return true, nil
	}
	return false, nil
}

func (s *Store) List(context.Context) ([]registrations.Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	out := make([]registrations.Registration, 0, len(s.rows))
	for i := len(s.rows) - 1; i >= 0; i-- {
		out = append(out, s.withLogin(s.rows[i]))
	}
	return out, nil
}

func (s *Store) SetCheckedIn(_ context.Context, id uuid.UUID, checkedIn bool) (registrations.Registration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Writes++
	for i, r := range s.rows {
		if r.ID != id {
			continue
		}
		switch {
		case !checkedIn:
			s.rows[i].CheckedInAt = nil
		case r.CheckedInAt == nil:
			at := s.now
			s.rows[i].CheckedInAt = &at
		}
		return s.withLogin(s.rows[i]), nil
	}
	return registrations.Registration{}, registrations.ErrNotFound
}

func (s *Store) withLogin(r registrations.Registration) registrations.Registration {
	if r.UserID != nil {
		r.Login = s.Logins[*r.UserID]
	}
	return r
}

// Accounts creates accounts in memory; a login in Taken already has one.
type Accounts struct {
	mu      sync.Mutex
	Taken   map[string]bool
	Created []registrations.NewAccount
	// TakenEmails are held by an account under another login.
	TakenEmails map[string]bool
	// Hashes counts the password hashes spent, as the real adapter spends
	// them: one for a new account, one for a taken email (found after the
	// hash) and, with mail on, one for a taken login or a repeat (a decoy).
	Hashes int
	// Err, when set, is every Create's answer.
	Err error
	// Store, when set, learns each created account's login for its join.
	Store *Store
}

var _ registrations.Accounts = (*Accounts)(nil)

func (a *Accounts) Create(_ context.Context, n registrations.NewAccount) (registrations.Account, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Err != nil {
		return registrations.Account{}, a.Err
	}
	if a.Taken[n.Login] {
		if n.ByEmail {
			a.Hashes++
		}
		return registrations.Account{Existing: true}, nil
	}
	a.Hashes++
	if a.TakenEmails[n.Email] {
		return registrations.Account{Existing: true}, nil
	}
	a.Created = append(a.Created, n)
	if a.Taken == nil {
		a.Taken = map[string]bool{}
	}
	a.Taken[n.Login] = true
	account := registrations.Account{ID: uuid.New(), Login: n.Login, OneTimePassword: "one-time-" + n.Login}
	if a.Store != nil {
		a.Store.mu.Lock()
		a.Store.Logins[account.ID] = account.Login
		a.Store.mu.Unlock()
	}
	return account, nil
}

func (a *Accounts) Decoy(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Hashes++
	return nil
}

// Outbox records what was queued.
type Outbox struct {
	mu       sync.Mutex
	Off      bool
	Messages []mailing.Message
	Wakes    int
}

var _ registrations.Outbox = (*Outbox)(nil)

func (o *Outbox) Enabled() bool { return !o.Off }

func (o *Outbox) EnqueueMany(_ context.Context, ms []mailing.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	o.Messages = append(o.Messages, ms...)
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
	return slices.Clone(o.Messages)
}

// Settings answers with Values, or Err when set.
type Settings struct {
	Values settings.Values
	Err    error
}

func (s Settings) All(context.Context) (settings.Values, error) { return s.Values, s.Err }

// UnitOfWork runs fn without a transaction, marking its context as inside
// one.
type UnitOfWork struct{}

func (UnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, txKey{}, true))
}

type txKey struct{}

// Sink collects audit entries, noting which were written outside a unit of
// work.
type Sink struct {
	mu      sync.Mutex
	Entries []audit.Entry
	Loose   []audit.Entry
}

func (s *Sink) Append(ctx context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Entries = append(s.Entries, e)
	if inTx, _ := ctx.Value(txKey{}).(bool); !inTx {
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

// Fixture is a service over fresh stand-ins, with mail on.
type Fixture struct {
	Service  *registrations.Service
	Store    *Store
	Accounts *Accounts
	Outbox   *Outbox
	Audit    *Sink
}

// NewFixture builds the service; configure, when given, adjusts the config
// before the service is made.
func NewFixture(configure func(*registrations.Config)) *Fixture {
	f := &Fixture{Store: NewStore(), Outbox: &Outbox{}, Audit: &Sink{}}
	f.Accounts = &Accounts{Store: f.Store}
	cfg := registrations.Config{
		Store: f.Store, Accounts: f.Accounts, Outbox: f.Outbox,
		Texts: mailing.NewTexts("https://olympdb.usm.md"),
		Settings: Settings{Values: settings.Values{
			settings.KeyName: "Olimpiada USM", settings.KeyMailLanguage: string(mailing.English),
		}},
		Audit:      audit.New(f.Audit),
		UnitOfWork: UnitOfWork{},
	}
	if configure != nil {
		configure(&cfg)
	}
	f.Service = registrations.NewService(cfg)
	return f
}
