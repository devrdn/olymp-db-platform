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

// Why an account's sign-in details were not emailed although they were
// asked for. A closed vocabulary the client translates; the password is
// issued either way.
const (
	EmailSkipNoAddress = "no_address"
	// EmailSkipNotActive keeps sign-in details from an account that cannot
	// sign in with them, such as a blocked one.
	EmailSkipNotActive = "not_active"
)

// Outbox queues email (mailing.Outbox). Both writes join the transaction in
// ctx, so a message commits or rolls back with the password it carries.
type Outbox interface {
	Enabled() bool
	// EnqueueMany stores the messages without waking the sender.
	EnqueueMany(ctx context.Context, ms []mailing.Message) error
	// AbandonPending gives up the accounts' pending messages of kind and
	// erases them, whether mail is on or not.
	AbandonPending(ctx context.Context, kind mailing.Kind, users []uuid.UUID, reason string) error
	// Wake tells the sender a committed message is waiting.
	Wake()
}

// Why a pending sign-in message was given up, kept with it in the outbox.
const (
	withdrawnSuperseded = "superseded: a newer one-time password was issued"
	withdrawnClosed     = "the account was blocked or deleted"
)

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
	// SkipReason is why they were not although asked for: EmailSkipNotActive
	// or EmailSkipNoAddress.
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
// returns a nil message when email was not asked for, or the account is not
// active or has no address.
//
// An error is the installation's fault, never the account's: the login, name
// and address were validated on the way in, the installation's name and the
// mail language are validated settings, and the password is ours. So no
// caller turns it into a skipped account; it changes nothing, and an import
// stops at that row as for an outage (CLAUDE.md rule 8).
func (l *letterhead) signIn(u User, password string) (*mailing.Message, EmailOutcome, error) {
	if l == nil {
		return nil, EmailOutcome{}, nil
	}
	if !u.IsActive() {
		return nil, EmailOutcome{SkipReason: EmailSkipNotActive}, nil
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
	if len(messages) == 0 {
		return nil
	}
	if err := s.mail.Outbox.EnqueueMany(ctx, messages); err != nil {
		return fmt.Errorf("queue the sign-in details: %w", err)
	}
	entries := make([]audit.Entry, 0, len(messages))
	for _, m := range messages {
		entries = append(entries, s.entry(actorID, audit.ActionUserSignInDetailsEmailed, m.UserID, nil))
	}
	return s.audit.RecordMany(ctx, entries)
}

// withdraw gives up the accounts' pending sign-in messages inside the unit of
// work that makes them wrong: a new password leaves the old one dead, and a
// blocked or deleted account cannot sign in with it.
func (s *Service) withdraw(ctx context.Context, ids []uuid.UUID, reason string) error {
	if s.mail == nil {
		return nil
	}
	return s.mail.Outbox.AbandonPending(ctx, mailing.KindSignInDetails, ids, reason)
}

// keepActive drops the messages of accounts that stopped being active since
// they were first read, and names them. It runs in the unit of work after the
// password write, whose row locks make a block that commits later wait for
// this one, while a block committed earlier is read here.
func (s *Service) keepActive(ctx context.Context, messages []mailing.Message) ([]mailing.Message, map[uuid.UUID]bool, error) {
	if len(messages) == 0 {
		return nil, nil, nil
	}
	ids := make([]uuid.UUID, len(messages))
	for i, m := range messages {
		ids[i] = m.UserID
	}
	current, err := s.repo.ByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	active := make(map[uuid.UUID]bool, len(current))
	for _, u := range current {
		active[u.ID] = u.IsActive()
	}
	kept := make([]mailing.Message, 0, len(messages))
	dropped := map[uuid.UUID]bool{}
	for _, m := range messages {
		if active[m.UserID] {
			kept = append(kept, m)
		} else {
			dropped[m.UserID] = true
		}
	}
	return kept, dropped, nil
}

// wake runs once the unit of work that queued a message has committed: until
// then the sender cannot see it.
func (s *Service) wake() {
	s.mail.Outbox.Wake()
}
