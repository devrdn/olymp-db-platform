package registrations

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

// Store keeps the registrations (internal/postgres). Every method joins the
// transaction ctx carries.
type Store interface {
	// ByEmailKey returns ErrNotFound for an email never registered.
	ByEmailKey(ctx context.Context, emailKey string) (Registration, error)
	Count(ctx context.Context) (int, error)
	// Insert stores r with a fresh id, refusing ErrAlreadyRegistered for a
	// held email, ErrFull once capacity rows exist and ErrCodeTaken for a
	// held code. sent stamps last_sent_at. No refusal aborts the
	// transaction in ctx.
	Insert(ctx context.Context, r Registration, capacity int, sent bool) (Registration, error)
	// AttachAccount links the email's registration to userID unless it
	// already has an account; ErrNotFound when no registration was linked.
	AttachAccount(ctx context.Context, emailKey string, userID uuid.UUID) (Registration, error)
	// ClaimResend stamps last_sent_at and reports true unless the code was
	// mailed within cooldown.
	ClaimResend(ctx context.Context, id uuid.UUID, cooldown time.Duration) (bool, error)
	// List returns every registration, newest first.
	List(ctx context.Context) ([]Registration, error)
	// SetCheckedIn records or clears the arrival, keeping an earlier arrival's
	// time; ErrNotFound for an unknown id.
	SetCheckedIn(ctx context.Context, id uuid.UUID, checkedIn bool) (Registration, error)
}

// NewAccount is the participant account a first registration asks for.
type NewAccount struct {
	Login    string
	Email    string
	FullName string
	// ByEmail also mails the sign-in details; only while mail is on.
	ByEmail bool
}

// Account is what Accounts.Create made. Existing means the login or the email
// already belonged to an account, and nothing was made.
type Account struct {
	ID              uuid.UUID
	Login           string
	OneTimePassword string
	Existing        bool
}

// Accounts creates participant accounts (users.Service, adapted in
// internal/app). Create runs its own transaction: a taken login or email
// aborts it, and must not abort the registration with it.
type Accounts interface {
	Create(ctx context.Context, n NewAccount) (Account, error)
	// Decoy spends what Create spends on a new account's password and makes
	// nothing, so a caller that made no account takes as long as one that did.
	Decoy(ctx context.Context) error
}

// Outbox queues email (mailing.Outbox).
type Outbox interface {
	Enabled() bool
	// EnqueueMany joins the transaction in ctx and wakes nobody.
	EnqueueMany(ctx context.Context, ms []mailing.Message) error
	Wake()
}

// Texts writes the code message (mailing.Texts).
type Texts interface {
	EventRegistration(lang mailing.Language, data mailing.EventRegistrationData) (mailing.Text, error)
}

// Settings reads the installation's name and mail language (settings.Service).
type Settings interface {
	All(ctx context.Context) (settings.Values, error)
}

type Config struct {
	Store      Store
	Accounts   Accounts
	Outbox     Outbox
	Texts      Texts
	Settings   Settings
	Audit      *audit.Recorder
	UnitOfWork storage.UnitOfWork
	// Random draws the codes; nil is crypto/rand.
	Random io.Reader
}

type Service struct {
	cfg Config
}

// NewService panics on a missing part, so a wiring fault stops the boot.
func NewService(cfg Config) *Service {
	if cfg.Store == nil || cfg.Accounts == nil || cfg.Outbox == nil || cfg.Texts == nil ||
		cfg.Settings == nil || cfg.Audit == nil || cfg.UnitOfWork == nil {
		panic("registrations: NewService needs every part of its Config")
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg}
}

type Status string

const (
	StatusCreated  Status = "created"
	StatusExisting Status = "existing"
)

// Outcome is what Register did. Account is zero for a repeat.
type Outcome struct {
	Status       Status
	Registration Registration
	Account      Account
	// EmailSent means the code was queued: false with mail off, or for a
	// repeat within the cooldown.
	EmailSent bool
	// MailOn is true whenever the installation sends mail, queued or not: the
	// person's details travel by email, so the answer need not carry them.
	MailOn bool
}

// codeAttempts is how many codes a registration draws before giving up. With
// 31^6 codes and at most Capacity held, even two collisions in a row have a
// probability near 3e-11.
const codeAttempts = 5

// Register records a, or mails the code again for an email already
// registered; a repeat never touches the account.
func (s *Service) Register(ctx context.Context, a Applicant) (Outcome, error) {
	out, err := s.register(ctx, a)
	out.MailOn = err == nil && s.cfg.Outbox.Enabled()
	return out, err
}

func (s *Service) register(ctx context.Context, a Applicant) (Outcome, error) {
	existing, err := s.cfg.Store.ByEmailKey(ctx, a.EmailKey)
	if err == nil {
		return s.resend(ctx, existing)
	}
	if !errors.Is(err, ErrNotFound) {
		return Outcome{}, err
	}

	// Checked before the account is made, so a full event creates none; the
	// insert checks again under its lock.
	count, err := s.cfg.Store.Count(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if count >= Capacity {
		return Outcome{}, ErrFull
	}

	// Read before the account is made, so a settings failure leaves none.
	mailOn := s.cfg.Outbox.Enabled()
	var lang mailing.Language
	var installation string
	if mailOn {
		if lang, installation, err = s.letterhead(ctx); err != nil {
			return Outcome{}, err
		}
	}
	account, err := s.cfg.Accounts.Create(ctx, NewAccount{
		Login: a.EmailKey, Email: a.Email, FullName: a.FullName(), ByEmail: mailOn,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("create the account: %w", err)
	}
	if account.Existing && mailOn {
		if err := s.cfg.Accounts.Decoy(ctx); err != nil {
			return Outcome{}, fmt.Errorf("spend the decoy hash: %w", err)
		}
	}

	r := Registration{FirstName: a.FirstName, LastName: a.LastName, Email: a.Email, EmailKey: a.EmailKey}
	if !account.Existing {
		r.UserID = &account.ID
		r.Login = account.Login
	}

	for range codeAttempts {
		if r.Code, err = s.drawCode(); err != nil {
			return Outcome{}, err
		}
		var message *mailing.Message
		if mailOn {
			if message, err = s.codeMessage(r, lang, installation, !account.Existing); err != nil {
				return Outcome{}, err
			}
		}
		var stored Registration
		err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
			var err error
			if stored, err = s.cfg.Store.Insert(ctx, r, Capacity, message != nil); err != nil {
				return err
			}
			if message == nil {
				return nil
			}
			return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{*message})
		})
		switch {
		case errors.Is(err, ErrCodeTaken):
			continue
		case errors.Is(err, ErrAlreadyRegistered):
			return s.lostTheRace(ctx, a, account)
		case err != nil:
			return Outcome{}, err
		}
		if message != nil {
			s.cfg.Outbox.Wake()
		}
		stored.Login = r.Login
		return Outcome{Status: StatusCreated, Registration: stored, Account: account, EmailSent: message != nil}, nil
	}
	return Outcome{}, fmt.Errorf("draw a free code in %d attempts: %w", codeAttempts, err)
}

// lostTheRace answers a registration another request of the same email
// stored first. When this request made the account, the other one found it
// taken and stored no account, so the account is attached and its password,
// shown nowhere else, is answered here.
func (s *Service) lostTheRace(ctx context.Context, a Applicant, account Account) (Outcome, error) {
	if !account.Existing {
		attached, err := s.cfg.Store.AttachAccount(ctx, a.EmailKey, account.ID)
		if err == nil {
			attached.Login = account.Login
			return Outcome{
				Status: StatusCreated, Registration: attached, Account: account,
				EmailSent: s.cfg.Outbox.Enabled() && attached.LastSentAt != nil,
			}, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Outcome{}, err
		}
	}
	existing, err := s.cfg.Store.ByEmailKey(ctx, a.EmailKey)
	if err != nil {
		return Outcome{}, err
	}
	return s.resend(ctx, existing)
}

// resend mails r's code again unless mail is off or the cooldown holds it.
func (s *Service) resend(ctx context.Context, r Registration) (Outcome, error) {
	out := Outcome{Status: StatusExisting, Registration: r}
	if !s.cfg.Outbox.Enabled() {
		return out, nil
	}
	// The answer to a repeat is the answer to a new address, so it costs the
	// same time.
	if err := s.cfg.Accounts.Decoy(ctx); err != nil {
		return Outcome{}, fmt.Errorf("spend the decoy hash: %w", err)
	}
	lang, installation, err := s.letterhead(ctx)
	if err != nil {
		return Outcome{}, err
	}
	message, err := s.codeMessage(r, lang, installation, false)
	if err != nil {
		return Outcome{}, err
	}
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		claimed, err := s.cfg.Store.ClaimResend(ctx, r.ID, ResendCooldown)
		if err != nil || !claimed {
			return err
		}
		out.EmailSent = true
		return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{*message})
	})
	if err != nil {
		return Outcome{}, err
	}
	if out.EmailSent {
		s.cfg.Outbox.Wake()
	}
	return out, nil
}

func (s *Service) letterhead(ctx context.Context) (mailing.Language, string, error) {
	values, err := s.cfg.Settings.All(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read the mail settings: %w", err)
	}
	return mailing.Language(values[settings.KeyMailLanguage]), values[settings.KeyName], nil
}

func (s *Service) codeMessage(r Registration, lang mailing.Language, installation string, newAccount bool) (*mailing.Message, error) {
	text, err := s.cfg.Texts.EventRegistration(lang, mailing.EventRegistrationData{
		Installation: installation,
		FullName:     r.FullName(),
		Code:         DisplayCode(r.Code),
		Login:        r.Login,
		NewAccount:   newAccount,
	})
	if err != nil {
		return nil, fmt.Errorf("write the code message: %w", err)
	}
	return &mailing.Message{
		Kind: mailing.KindEventRegistration, UserID: uuid.Nil, To: r.Email,
		Subject: text.Subject, Body: text.Body,
	}, nil
}

// drawCode rejects bytes past the last whole multiple of the alphabet, so
// every letter is equally likely.
func (s *Service) drawCode() (string, error) {
	const limit = 256 - 256%len(CodeAlphabet)
	code := make([]byte, 0, CodeLength)
	var b [1]byte
	for len(code) < CodeLength {
		if _, err := io.ReadFull(s.cfg.Random, b[:]); err != nil {
			return "", fmt.Errorf("draw a code: %w", err)
		}
		if int(b[0]) >= limit {
			continue
		}
		code = append(code, CodeAlphabet[int(b[0])%len(CodeAlphabet)])
	}
	return string(code), nil
}

// List returns every registration, newest first; Capacity bounds them.
func (s *Service) List(ctx context.Context) ([]Registration, error) {
	return s.cfg.Store.List(ctx)
}

func (s *Service) SetCheckedIn(ctx context.Context, actor, id uuid.UUID, checkedIn bool) (Registration, error) {
	var updated Registration
	err := s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		var err error
		if updated, err = s.cfg.Store.SetCheckedIn(ctx, id, checkedIn); err != nil {
			return err
		}
		return s.cfg.Audit.Record(ctx, audit.Entry{
			ActorID: &actor, Action: audit.ActionEventRegistrationCheckIn,
			Entity: "event_registration", EntityID: id.String(),
			Payload: map[string]any{"checked_in": checkedIn},
		})
	})
	if err != nil {
		return Registration{}, err
	}
	return updated, nil
}
