package recovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/mailing"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/settings"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
)

// Store keeps the links waiting to be followed (internal/postgres). Every
// method joins the transaction ctx carries; times are the caller's clock.
type Store interface {
	// Replace stores l as its account's one link, in place of any earlier.
	Replace(ctx context.Context, l Link) error
	// Find reports whether a link of tokenHash works at now.
	Find(ctx context.Context, tokenHash []byte, now time.Time) (bool, error)
	// Spend deletes the link of tokenHash and returns it; false when there is
	// none. Of two spends of one link, only one finds it.
	Spend(ctx context.Context, tokenHash []byte) (Link, bool, error)
	// Sweep deletes every link expired at now.
	Sweep(ctx context.Context, now time.Time) (int64, error)
}

// Directory finds accounts (users.Repository).
type Directory interface {
	// ByLogin may return a deleted account.
	ByLogin(ctx context.Context, login string) (users.User, error)
	// ByEmail ignores case and finds live accounts only.
	ByEmail(ctx context.Context, email string) (users.User, error)
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
	// Lock holds the account's row until the transaction in ctx ends, which
	// ctx must carry; ErrNotFound when there is none.
	Lock(ctx context.Context, id uuid.UUID) error
	users.ContestManagers
}

// Accounts sets the chosen password (users.Service).
type Accounts interface {
	SetPasswordByLink(ctx context.Context, cmd users.PasswordByLinkCommand) (bool, error)
}

// Hasher hashes the chosen password (the sign-in hasher, password.Hasher).
type Hasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

// Outbox queues email (mailing.Outbox).
type Outbox interface {
	Enabled() bool
	// EnqueueMany joins the transaction in ctx and wakes nobody.
	EnqueueMany(ctx context.Context, ms []mailing.Message) error
	Wake()
}

// Texts writes the message (mailing.Texts).
type Texts interface {
	PasswordReset(lang mailing.Language, data mailing.PasswordResetData) (mailing.Text, error)
}

// Settings reads the installation's name and mail language (settings.Service).
type Settings interface {
	All(ctx context.Context) (settings.Values, error)
}

// MailBudget is the installation's budget of reset links (mailing.Budget,
// sized by MailsPerMinute and MailsPerDay).
type MailBudget interface {
	// Spend counts one message and reports whether the budget admits it.
	Spend(ctx context.Context) (bool, error)
}

// Limiter counts attempts per subject in a fixed window (auth.Limiter).
type Limiter interface {
	Spend(ctx context.Context, subject string, limit int, window time.Duration) (allowed, firstRefused bool, err error)
}

// The budgets every request for a link spends at Submit, whatever it names,
// refusals included (CLAUDE.md rule 13). MailsPerMinute and MailsPerDay are
// the installation's, apart from sign-up's so a flood of sign-ups cannot
// shut recovery; with sign-up's twelve they leave the relay's default 25 a
// minute room for staff mail. MailsPerAddressPerMinute and
// MailsPerAddressPerDay are one caller's address's share, so one machine
// cannot hold the installation's minute at zero.
const (
	MailsPerMinute           = 6
	MailsPerDay              = 2000
	MailsPerAddressPerMinute = 2
	MailsPerAddressPerDay    = 100
)

type Config struct {
	Store      Store
	Directory  Directory
	Accounts   Accounts
	Hasher     Hasher
	Outbox     Outbox
	Texts      Texts
	Settings   Settings
	Limiter    Limiter
	MailBudget MailBudget
	UnitOfWork storage.UnitOfWork
	Log        *slog.Logger
	// PublicURL is the address the installation is reached at, which a link
	// opens.
	PublicURL string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Random draws tokens; nil is crypto/rand.
	Random io.Reader
}

type Service struct {
	cfg   Config
	slots chan struct{}

	// mu orders Submit's Add against Stop's Wait.
	mu      sync.Mutex
	stopped bool
	running sync.WaitGroup
}

// NewService panics on a missing part, so a wiring fault stops the boot.
func NewService(cfg Config) *Service {
	if cfg.Store == nil || cfg.Directory == nil || cfg.Accounts == nil || cfg.Hasher == nil ||
		cfg.Outbox == nil || cfg.Texts == nil || cfg.Settings == nil || cfg.Limiter == nil || cfg.MailBudget == nil ||
		cfg.UnitOfWork == nil || cfg.Log == nil {
		panic("recovery: NewService needs every part of its Config")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg, slots: make(chan struct{}, MaxInFlight)}
}

const (
	// MaxInFlight bounds the requests Submit runs at once; past it a request
	// is dropped, so a burst cannot hold more database connections than this.
	MaxInFlight = 8
	// RequestTimeout bounds one request Submit runs.
	RequestTimeout = 30 * time.Second
)

// RequestCommand is a request for a link. Lang is the reader's language;
// one the texts are not written in falls back to the installation's. Sender
// is the caller's address as a rate-limit subject (httpx.ClientSubject);
// callers with none share one.
type RequestCommand struct {
	LoginOrEmail, Lang, Sender string
}

// Submit spends the budgets, then runs Request in the background and returns
// at once, so the caller answers before anything is looked up and alike for
// every account. Every request spends the budgets, whatever it names: a
// budget spent only for a link that is mailed would let a caller watching
// their own link learn whether another address has an account. A refusal
// is ErrTooManyLinks or ErrMailBudget. It reports whether the request was
// taken: one past MaxInFlight, or after Stop, is dropped with a warning and
// nil. The request keeps ctx's values but not its cancellation, and its
// failure is logged, since nobody waits for it.
func (s *Service) Submit(ctx context.Context, cmd RequestCommand) (bool, error) {
	if err := s.admit(ctx, cmd.Sender); err != nil {
		return false, err
	}
	return s.handOff(ctx, cmd), nil
}

// admit spends the caller's address's share, its minute then its day, and
// then the installation's budget; a refusal charges nothing after it, and
// the address is the more bounded key (CLAUDE.md rule 5).
func (s *Service) admit(ctx context.Context, sender string) error {
	for _, window := range []struct {
		name   string
		limit  int
		length time.Duration
	}{
		{"minute", MailsPerAddressPerMinute, time.Minute},
		{"day", MailsPerAddressPerDay, 24 * time.Hour},
	} {
		allowed, _, err := s.cfg.Limiter.Spend(ctx, "recovery_mail:ip:"+sender+":"+window.name, window.limit, window.length)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrTooManyLinks
		}
	}
	allowed, err := s.cfg.MailBudget.Spend(ctx)
	if err != nil {
		return fmt.Errorf("spend the recovery mail budget: %w", err)
	}
	if !allowed {
		return ErrMailBudget
	}
	return nil
}

func (s *Service) handOff(ctx context.Context, cmd RequestCommand) bool {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.cfg.Log.WarnContext(ctx, "dropped a password reset request: the service is stopping")
		return false
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.mu.Unlock()
		s.cfg.Log.WarnContext(ctx, "dropped a password reset request: too many in flight", "limit", MaxInFlight)
		return false
	}
	s.running.Add(1)
	s.mu.Unlock()

	go func() {
		defer func() {
			<-s.slots
			s.running.Done()
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RequestTimeout)
		defer cancel()
		if err := s.Request(ctx, cmd.LoginOrEmail, cmd.Lang); err != nil {
			s.cfg.Log.ErrorContext(ctx, "a password reset request failed", "error", err)
		}
	}()
	return true
}

// Stop takes no more requests and waits for those in flight until ctx ends.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for the password reset requests in flight: %w", ctx.Err())
	}
}

// Request mails a link to the account loginOrEmail names, an email when it
// holds an @, in place of any link mailed to it before. An account a link
// may not reach (unknown, deleted, blocked, staff or without an address)
// gets nothing and the same nil, after the same lookup, draw and settings
// read; no path hashes a password. It spends no budget: Submit did.
func (s *Service) Request(ctx context.Context, loginOrEmail, lang string) error {
	account, reaches, err := s.find(ctx, loginOrEmail)
	if err != nil {
		return err
	}
	token, tokenHash, err := s.drawToken()
	if err != nil {
		return err
	}
	language, installation, err := s.letterhead(ctx, lang)
	if err != nil {
		return err
	}
	if !reaches {
		return nil
	}

	text, err := s.cfg.Texts.PasswordReset(language, mailing.PasswordResetData{
		Installation: installation, FullName: account.FullName, Login: account.Login,
		Link: strings.TrimRight(s.cfg.PublicURL, "/") + ResetPath + token,
	})
	if err != nil {
		return fmt.Errorf("write the reset message: %w", err)
	}
	link := Link{
		TokenHash: tokenHash, UserID: account.ID, Email: strings.ToLower(account.Email),
		ExpiresAt: s.cfg.Now().Add(LinkLifetime),
	}
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		if err := s.cfg.Store.Replace(ctx, link); err != nil {
			return err
		}
		return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{{
			Kind: mailing.KindPasswordReset, UserID: account.ID, To: account.Email,
			Subject: text.Subject, Body: text.Body,
		}})
	})
	if err != nil {
		return err
	}
	s.cfg.Outbox.Wake()
	return nil
}

// find resolves what a person typed to an account and reports whether a
// link may reach it. Anything longer than an address names no account.
func (s *Service) find(ctx context.Context, loginOrEmail string) (users.User, bool, error) {
	typed := strings.ToLower(strings.TrimSpace(loginOrEmail))
	if typed == "" || len(typed) > users.MaxEmailLength {
		return users.User{}, false, nil
	}
	var account users.User
	var err error
	if strings.Contains(typed, "@") {
		account, err = s.cfg.Directory.ByEmail(ctx, typed)
	} else {
		account, err = s.cfg.Directory.ByLogin(ctx, typed)
	}
	switch {
	case errors.Is(err, users.ErrNotFound):
		return users.User{}, false, nil
	case err != nil:
		return users.User{}, false, err
	}
	reaches, err := users.ReachableByEmail(ctx, s.cfg.Directory, account)
	return account, reaches, err
}

// Complete sets newPassword on the account of token's link, marks its
// address verified and ends every session, spending the link. A malformed
// token is refused before the store, and an unknown or expired one before
// the hash. The link is spent and the account checked again inside the
// transaction that sets the password, so a link is used once, and one whose
// account has since become staff, been blocked or deleted, or changed its
// address is refused and deleted.
func (s *Service) Complete(ctx context.Context, token, newPassword string) error {
	tokenHash, ok := digest(token)
	if !ok {
		return ErrInvalidLink
	}
	live, err := s.cfg.Store.Find(ctx, tokenHash, s.cfg.Now())
	if err != nil {
		return err
	}
	if !live {
		return ErrInvalidLink
	}
	if err := users.ValidatePassword(newPassword); err != nil {
		return err
	}
	passwordHash, err := s.cfg.Hasher.Hash(ctx, newPassword)
	if err != nil {
		return fmt.Errorf("hash the password: %w", err)
	}

	// Taken after the hash, which may wait for a slot.
	now := s.cfg.Now()
	done, err := s.cfg.Accounts.SetPasswordByLink(ctx, users.PasswordByLinkCommand{
		PasswordHash: passwordHash, At: now,
		Claim: func(ctx context.Context) (uuid.UUID, bool, error) {
			link, ok, err := s.cfg.Store.Spend(ctx, tokenHash)
			if err != nil || !ok || !now.Before(link.ExpiresAt) {
				return uuid.Nil, false, err
			}
			// Read once the row is held, so a change to the address or the
			// status either commits before the read or waits for this one; a
			// role change waits only at its session bump (users.Repository).
			err = s.cfg.Directory.Lock(ctx, link.UserID)
			if errors.Is(err, users.ErrNotFound) {
				return uuid.Nil, false, nil
			}
			if err != nil {
				return uuid.Nil, false, err
			}
			account, err := s.cfg.Directory.ByID(ctx, link.UserID)
			switch {
			case errors.Is(err, users.ErrNotFound):
				return uuid.Nil, false, nil
			case err != nil:
				return uuid.Nil, false, err
			}
			reaches, err := users.ReachableByEmail(ctx, s.cfg.Directory, account)
			if err != nil {
				return uuid.Nil, false, err
			}
			if !reaches || strings.ToLower(account.Email) != link.Email {
				return uuid.Nil, false, nil
			}
			return account.ID, true, nil
		},
	})
	if err != nil {
		return err
	}
	if !done {
		return ErrInvalidLink
	}
	return nil
}

// Sweep deletes the links expired at now; an expired one already opens
// nothing, so this only reclaims the rows.
func (s *Service) Sweep(ctx context.Context, now time.Time) (int64, error) {
	return s.cfg.Store.Sweep(ctx, now)
}

// Offered reports whether a link can reach anyone: mail is on.
func (s *Service) Offered() bool { return s.cfg.Outbox.Enabled() }

// letterhead picks the reader's language when the texts are written in it,
// the installation's otherwise.
func (s *Service) letterhead(ctx context.Context, lang string) (mailing.Language, string, error) {
	values, err := s.cfg.Settings.All(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read the mail settings: %w", err)
	}
	language := mailing.Language(lang)
	if !slices.Contains(mailing.Languages(), language) {
		language = mailing.Language(values[settings.KeyMailLanguage])
	}
	return language, values[settings.KeyName], nil
}

// drawToken returns a token for the link and its SHA-256, which is all that
// is stored: the token is random enough to need no salt.
func (s *Service) drawToken() (string, []byte, error) {
	raw := make([]byte, TokenBytes)
	if _, err := io.ReadFull(s.cfg.Random, raw); err != nil {
		return "", nil, fmt.Errorf("draw a token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	return token, h[:], nil
}

// digest is the SHA-256 the store keys token by, or false for a token no
// draw could make.
func digest(token string) ([]byte, bool) {
	if len(token) != TokenLength {
		return nil, false
	}
	if _, err := base64.RawURLEncoding.Strict().DecodeString(token); err != nil {
		return nil, false
	}
	h := sha256.Sum256([]byte(token))
	return h[:], true
}
