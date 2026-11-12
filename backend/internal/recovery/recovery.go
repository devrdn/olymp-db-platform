// Package recovery lets the owner of a student account who forgot its
// password set a new one through a single-use link mailed to the account's
// address. Following the link proves the inbox, so completing it also marks
// the address verified, and it ends every session. A request is answered
// alike whether or not an account was reached: unknown, staff, deleted,
// blocked and email-less accounts get nothing, and staff never get a link.
//
// It does not serve HTTP or throttle callers (internal/api does, before the
// lookup and before the password hash), and does not sign the person in:
// they sign in with the new password.
package recovery

import (
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// TokenBytes is the randomness of a link's token.
	TokenBytes = 32
	// LinkLifetime is how long a link works, from when it was mailed.
	LinkLifetime = 30 * time.Minute
	// ResetPath is the interface's page a link opens, before its token.
	ResetPath = "/reset?token="
)

// TokenLength is the length of every token a link carries; a string of any
// other length is no token.
var TokenLength = base64.RawURLEncoding.EncodedLen(TokenBytes)

// ErrInvalidLink is one answer for a link that is unknown, used, expired,
// or whose account can no longer be reached by email, so a token says
// nothing about which it was.
var ErrInvalidLink = errors.New("the reset link is unknown, used or expired")

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// users.ErrWeakPassword and the hasher's password.ErrBusy pass through and
// belong to those packages.
func Errors() []error {
	return []error{ErrInvalidLink}
}

// Link is a reset link waiting to be followed.
type Link struct {
	// TokenHash is SHA-256 of the token mailed; the token is not stored.
	TokenHash []byte
	UserID    uuid.UUID
	// Email is the lowercased address the link was mailed to: only that
	// address is proved by following it.
	Email     string
	ExpiresAt time.Time
}
