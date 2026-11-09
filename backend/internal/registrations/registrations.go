// Package registrations answers who signed up for the evening's event through
// the public form, under which entrance code, and who has arrived. A first
// registration also creates the participant's account (through Accounts) and
// emails the code through the outbox.
//
// It does not enroll anyone in a contest, which the contest's open enrollment
// does, and it does not decide whether the form is open: the deployment does,
// with EVENT_REGISTRATION_ENABLED.
package registrations

import (
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

const (
	// MaxNameRunes bounds a first or a last name.
	MaxNameRunes = 100
	// MaxFullNameBytes bounds the two names with the space between them,
	// which become the account's full name.
	MaxFullNameBytes = users.MaxFullNameLength
	// MaxEmailBytes bounds the address, typed and lowercased. It is lower
	// than an address may be because the lowercased address becomes the
	// account's login.
	MaxEmailBytes = users.MaxLoginLength
	// Capacity is how many registrations the event holds; a repeat of one
	// already held is still answered once it is full.
	Capacity = 5000
	// ResendCooldown is how long a registration's code is not mailed again,
	// so the public form cannot flood one inbox.
	ResendCooldown = 5 * time.Minute
)

var (
	ErrInvalidName  = errors.New("a name must be 1 to 100 letters, spaces, hyphens, apostrophes or periods, and the two at most 200 bytes together")
	ErrInvalidEmail = errors.New("the email must be one address of at most 100 characters, without a name")
	ErrFull         = errors.New("the event has reached its 5000 registrations")
	ErrNotFound     = errors.New("no such registration")

	// ErrAlreadyRegistered is the store refusing an email it already holds.
	ErrAlreadyRegistered = errors.New("the email is already registered")
	// ErrCodeTaken is the store refusing a code another registration holds.
	ErrCodeTaken = errors.New("the code is already taken")
)

// Applicant is a registration form as Parse accepted it.
type Applicant struct {
	FirstName string
	LastName  string
	// Email is trimmed and otherwise as typed; EmailKey is it lowercased, the
	// registration's identity and the account's login.
	Email    string
	EmailKey string
}

func (a Applicant) FullName() string { return a.FirstName + " " + a.LastName }

// Parse trims and checks a registration form. It reads no storage, so a caller
// may run it before deciding whether the request is worth any.
func Parse(firstName, lastName, email string) (Applicant, error) {
	a := Applicant{
		FirstName: strings.TrimSpace(firstName),
		LastName:  strings.TrimSpace(lastName),
		Email:     strings.TrimSpace(email),
	}
	if !validName(a.FirstName) || !validName(a.LastName) || len(a.FullName()) > MaxFullNameBytes {
		return Applicant{}, ErrInvalidName
	}
	if !validEmail(a.Email) {
		return Applicant{}, ErrInvalidEmail
	}
	// Lowercasing can lengthen a letter in bytes.
	a.EmailKey = strings.ToLower(a.Email)
	if len(a.EmailKey) > MaxEmailBytes {
		return Applicant{}, ErrInvalidEmail
	}
	return a, nil
}

// validName takes only what a name is written with: the names greet the
// reader of two messages sent to any address, and must not carry a link or a
// pitch. A period directly followed by a letter is refused too, since mail
// clients turn "evil.com" into a link; "Jr." and "J. R. R." stay.
func validName(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= MaxNameRunes && utf8.ValidString(s) && !strings.ContainsFunc(s, notOfAName) && !periodBeforeLetter(s)
}

func periodBeforeLetter(s string) bool {
	afterPeriod := false
	for _, r := range s {
		if afterPeriod && unicode.IsLetter(r) {
			return true
		}
		afterPeriod = r == '.'
	}
	return false
}

func notOfAName(r rune) bool {
	switch r {
	case ' ', '-', '\'', '’', '.':
		return false
	}
	return !unicode.IsLetter(r) && !unicode.IsMark(r)
}

// validEmail wants the address to parse back to itself: net/mail also accepts
// a display name, a list's first entry and a quoted local part holding an @.
func validEmail(s string) bool {
	if len(s) > MaxEmailBytes || strings.Count(s, "@") != 1 || strings.ContainsFunc(s, unicode.IsControl) {
		return false
	}
	parsed, err := mail.ParseAddress(s)
	return err == nil && parsed.Name == "" && parsed.Address == s
}

// Registration is one person signed up for the event.
type Registration struct {
	ID        uuid.UUID
	FirstName string
	LastName  string
	Email     string
	EmailKey  string
	// Code is stored and returned without its separator; DisplayCode adds it.
	Code string
	// UserID is the account made with the registration, nil when the person
	// already had one.
	UserID *uuid.UUID
	// Login is the linked account's login, empty without one.
	Login       string
	CreatedAt   time.Time
	LastSentAt  *time.Time
	CheckedInAt *time.Time
}

func (r Registration) FullName() string { return r.FirstName + " " + r.LastName }

// CodeLength and CodeAlphabet make an entrance code: no O, 0, I, 1 or L, which
// are misread when the code is spoken or typed.
const (
	CodeLength   = 6
	CodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// DisplayCode is code as people read it, K7M-2QX.
func DisplayCode(code string) string {
	if len(code) != CodeLength {
		return code
	}
	return code[:3] + "-" + code[3:]
}
