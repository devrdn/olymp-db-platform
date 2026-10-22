// Package mailing queues email and delivers it in the background: an outbox a
// request writes a message to, inside its own transaction when it has one, and
// a dispatcher the background job runs, which claims due messages, hands them
// to the relay at a bounded rate, retries what a retry can help and abandons
// the rest. It does not speak SMTP (internal/platform/mail does), write the
// texts of messages, or decide who receives one.
package mailing

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/platform/mail"
	"github.com/google/uuid"
)

var (
	// ErrDisabled refuses a message while mail is off (SMTP_HOST is empty).
	ErrDisabled = errors.New("mail is off")
	// ErrInvalidMessage refuses a message the relay could not take as given.
	ErrInvalidMessage = errors.New("the message cannot be sent as given")
)

// Kind says what a message is for. It labels the metrics, so the set is
// fixed in code.
type Kind string

// KindSignInDetails carries an account's login and one-time password.
const KindSignInDetails Kind = "sign_in_details"

// Kinds is every kind the outbox accepts.
func Kinds() []Kind { return []Kind{KindSignInDetails} }

// KindNames is Kinds as label values.
func KindNames() []string {
	names := make([]string, 0, len(Kinds()))
	for _, kind := range Kinds() {
		names = append(names, string(kind))
	}
	return names
}

// Message is one plain-text email to one recipient.
type Message struct {
	Kind Kind
	// UserID is the recipient's account, uuid.Nil when the address belongs to
	// none.
	UserID uuid.UUID
	// To is one bare address, no display name.
	To      string
	Subject string
	// Body may hold a one-time password: it is stored until the message is
	// sent or abandoned, then erased, and never logged.
	Body string
}

// Validate checks the message against the relay's bounds
// (internal/platform/mail), so a message that could never be sent is refused
// when it is queued rather than abandoned later. Its errors never repeat the
// subject or the body.
func (m Message) Validate() error {
	if !slices.Contains(Kinds(), m.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidMessage, m.Kind)
	}
	if len(m.To) > mail.MaxAddressBytes || strings.ContainsRune(m.To, 0) {
		return fmt.Errorf("%w: the recipient is not one address of at most %d bytes", ErrInvalidMessage, mail.MaxAddressBytes)
	}
	if addr, err := netmail.ParseAddress(m.To); err != nil || addr.Name != "" || addr.Address != m.To {
		return fmt.Errorf("%w: the recipient is not one bare address", ErrInvalidMessage)
	}
	if err := checkText("subject", m.Subject, mail.MaxSubjectBytes); err != nil {
		return err
	}
	return checkText("body", m.Body, mail.MaxBodyBytes)
}

// checkText refuses an empty or oversized field, and bytes PostgreSQL's text
// cannot store.
func checkText(field, value string, maxBytes int) error {
	switch {
	case value == "":
		return fmt.Errorf("%w: the %s is empty", ErrInvalidMessage, field)
	case len(value) > maxBytes:
		return fmt.Errorf("%w: the %s is %d bytes, at most %d", ErrInvalidMessage, field, len(value), maxBytes)
	case !utf8.ValidString(value) || strings.ContainsRune(value, 0):
		return fmt.Errorf("%w: the %s is not UTF-8 text", ErrInvalidMessage, field)
	}
	return nil
}
