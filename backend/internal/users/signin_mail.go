package users

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/google/uuid"
)

// ErrMailOff refuses a request to email sign-in details while mail is off. It
// is refused before anything changes, so no account is created and no
// password is issued that its owner would never receive.
var ErrMailOff = errors.New("email is off, so sign-in details cannot be emailed")

// EmailSkipNoAddress is why an account's sign-in details were not emailed
// although they were asked for: it has no email address.
const EmailSkipNoAddress = "no_address"

// Outbox queues email (mailing.Outbox).
type Outbox interface {
	Enabled() bool
	// Enqueue joins the transaction in ctx, so the message commits or rolls
	// back with the password it carries.
	Enqueue(ctx context.Context, m mailing.Message) error
	// Wake tells the sender a committed message is waiting.
	Wake()
}

// SignInTexts writes the sign-in details message (mailing.Texts).
type SignInTexts interface {
	SignInDetails(lang mailing.Language, data mailing.SignInData) (mailing.Text, error)
}

// MailSettings reads the installation's name, contact address and mail
// language (settings.Service).
type MailSettings interface {
	All(ctx context.Context) (settings.Values, error)
}

// Mail is what emailing sign-in details needs.
type Mail struct {
	Outbox   Outbox
	Texts    SignInTexts
	Settings MailSettings
}

// WithMail is for the composition root, before the service is shared. A
// service without it refuses every request to email sign-in details with
// ErrMailOff. It panics on a missing part, so a wiring fault stops the boot.
func (s *Service) WithMail(m Mail) *Service {
	if m.Outbox == nil || m.Texts == nil || m.Settings == nil {
		panic("users: WithMail needs the outbox, the texts and the settings")
	}
	s.mail = &m
	return s
}

// Delivery is how an issued one-time password reaches its owner besides the
// answer to the administrator, who is always handed it. The zero value hands
// it to the administrator only.
type Delivery struct {
	// ByEmail also queues the sign-in details to the account's address.
	ByEmail bool
}

// EmailOutcome says whether an issued password was also emailed. Both fields
// are zero when email was not asked for.
type EmailOutcome struct {
	// Emailed means the sign-in details were queued in the transaction that
	// issued the password; delivery happens after it commits.
	Emailed bool
	// SkipReason is why they were not although asked for:
	// EmailSkipNoAddress.
	SkipReason string
}

// refuseMailOff is checked first by every operation that issues a password.
func (s *Service) refuseMailOff(d Delivery) error {
	if d.ByEmail && (s.mail == nil || !s.mail.Outbox.Enabled()) {
		return ErrMailOff
	}
	return nil
}

// letterhead is what every sign-in message of one request shares, read once
// per request. A nil letterhead means email was not asked for.
type letterhead struct {
	texts        SignInTexts
	language     mailing.Language
	installation string
	contact      string
}

// letterheadFor reads the settings the messages need when d asks for email.
// refuseMailOff has already passed.
func (s *Service) letterheadFor(ctx context.Context, d Delivery) (*letterhead, error) {
	if !d.ByEmail {
		return nil, nil
	}
	values, err := s.mail.Settings.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the mail settings: %w", err)
	}
	return &letterhead{
		texts:        s.mail.Texts,
		language:     mailing.Language(values[settings.KeyMailLanguage]),
		installation: values[settings.KeyName],
		contact:      values[settings.KeyContact],
	}, nil
}

// signIn writes an account's sign-in details, or says why it gets none. It
// returns a nil message when email was not asked for or the account has no
// address. An error means the message cannot be written for reasons that are
// not the account's: its login, name and address were validated on the way
// in, the installation's name is a required setting and the password is
// ours.
func (l *letterhead) signIn(u User, password string) (*mailing.Message, EmailOutcome, error) {
	if l == nil {
		return nil, EmailOutcome{}, nil
	}
	if u.Email == "" {
		return nil, EmailOutcome{SkipReason: EmailSkipNoAddress}, nil
	}
	text, err := l.texts.SignInDetails(l.language, mailing.SignInData{
		Installation: l.installation,
		FullName:     u.FullName,
		Login:        u.Login,
		Password:     password,
		Contact:      l.contact,
	})
	if err != nil {
		return nil, EmailOutcome{}, fmt.Errorf("write the sign-in details of %s: %w", u.Login, err)
	}
	m := &mailing.Message{
		Kind: mailing.KindSignInDetails, UserID: u.ID, To: u.Email,
		Subject: text.Subject, Body: text.Body,
	}
	if err := m.Validate(); err != nil {
		return nil, EmailOutcome{}, fmt.Errorf("write the sign-in details of %s: %w", u.Login, err)
	}
	return m, EmailOutcome{Emailed: true}, nil
}

// queue puts the messages in the outbox and records each one. It runs inside
// the unit of work that issues the passwords they carry.
func (s *Service) queue(ctx context.Context, actorID uuid.UUID, messages []mailing.Message) error {
	entries := make([]audit.Entry, 0, len(messages))
	for _, m := range messages {
		if err := s.mail.Outbox.Enqueue(ctx, m); err != nil {
			return fmt.Errorf("queue the sign-in details: %w", err)
		}
		entries = append(entries, s.entry(actorID, audit.ActionUserSignInDetailsEmailed, m.UserID, nil))
	}
	return s.audit.RecordMany(ctx, entries)
}

// wake runs once the unit of work that queued a message has committed: until
// then the sender cannot see it.
func (s *Service) wake() {
	s.mail.Outbox.Wake()
}
