// Package signup lets a person create their own account with a password: a
// sign-up waits, as a pending row and not an account, until the person types
// the six-digit code mailed to the address, in the browser that started it,
// and only then becomes a verified student account. An address that already
// has an account is answered exactly as a new one is, and its owner is mailed
// instead.
//
// It spends the mail budgets itself, just before a message is queued, so a
// request refused for anything else spends none. It does not serve HTTP or
// throttle requests (internal/api does, before the body is read, and keeps
// Start's token for the browser), does not sign the new account in (auth
// does), and does not decide whether sign-up is offered: the deployment
// does, with SELF_SIGNUP_ENABLED and mail.
package signup

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// CodeDigits is the length of a code, every digit drawn uniformly.
	CodeDigits = 6
	// CodeLifetime is how long a code confirms, from when it was mailed.
	CodeLifetime = 30 * time.Minute
	// MaxTries is how many tries a sign-up gets in all, however many codes it
	// is sent; the next try is answered as expired, the right code included.
	MaxTries = 5
	// ResendCooldown is how long after a code was mailed another may be.
	ResendCooldown = 60 * time.Second
	// MaxPending bounds the unexpired sign-ups held at once.
	MaxPending = 10_000
	// TokenBytes is the randomness of the token binding a sign-up to the
	// browser that started it.
	TokenBytes = 32
)

// The mail budgets Start and Resend spend, refusals included (CLAUDE.md rule
// 13). MailsPerMinute and MailsPerDay are the installation's, which with
// recovery's six a minute leave the relay's default 25 room for staff mail;
// MailsPerAddress is per minute for one caller's address, Start and Resend
// together, so one machine takes half the installation's minute at most;
// MailsPerEmail is per hour for one typed address, for Start and for Resend
// each.
const (
	MailsPerMinute  = 12
	MailsPerDay     = 5000
	MailsPerAddress = 6
	MailsPerEmail   = 5
)

var (
	// ErrNotAvailable means the installation does not offer sign-up: the
	// switch is off, or mail is, and no code could reach anyone.
	ErrNotAvailable = errors.New("self-service sign-up is not available")
	// ErrWrongCode also answers an address with no pending sign-up, so a
	// confirmation does not tell which addresses have one.
	ErrWrongCode   = errors.New("the code is not the one that was sent")
	ErrCodeExpired = errors.New("the code has expired or was tried too often; sign up again")
	ErrTooSoon     = errors.New("a code was sent less than a minute ago")
	ErrFull        = errors.New("too many sign-ups are waiting for their code; try later")
	// ErrMailBudget means the installation's budget of sign-up mail is spent
	// for the minute or the day, whoever asks.
	ErrMailBudget = errors.New("this site is sending too many messages just now; wait a few minutes before trying again")
	// ErrTooManyCodes means the caller's address or the typed one has asked
	// for as many codes as it may for now.
	ErrTooManyCodes = errors.New("too many codes were asked for from this address or for this email; wait before trying again")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// users.ErrInvalidAccount, ErrWeakPassword, ErrLoginTaken and ErrEmailTaken
// and the hasher's password.ErrBusy pass through and belong to those packages.
func Errors() []error {
	return []error{ErrNotAvailable, ErrWrongCode, ErrCodeExpired, ErrTooSoon, ErrFull, ErrMailBudget, ErrTooManyCodes}
}

// Pending is a sign-up waiting for its code.
type Pending struct {
	ID uuid.UUID
	// Email is trimmed and lowercased: one pending sign-up per address.
	Email    string
	Login    string
	FullName string
	// PasswordHash is the chosen password, hashed when the sign-up started.
	PasswordHash string
	// CodeHash is a random salt followed by SHA-256 of the salt and the code.
	CodeHash []byte
	// TokenHash is SHA-256 of the token Start answered.
	TokenHash []byte
	Tries     int
	SentAt    time.Time
	ExpiresAt time.Time
}

// spent reports whether the code can no longer confirm at now.
func (p Pending) spent(now time.Time) bool {
	return p.Tries >= MaxTries || !now.Before(p.ExpiresAt)
}
