// Package federation decides which account a person signing in through an
// external identity provider enters: the account the identity is already
// linked to, a live student account whose address the provider vouches for,
// or a new account.
//
// It knows no provider. An adapter verifies the provider's token, decides
// whether the email in it is trusted, and hands over Claims; another provider
// needs another adapter and no change here. It does not serve HTTP, open the
// session (auth does) or throttle callers (internal/api does).
//
// Only an ASCII address finds, links or lands on an account. Case folding
// beyond ASCII, in Go and in PostgreSQL's lower(), takes U+212A KELVIN SIGN
// for k, so one spelling would find the account of another; an email with any
// other character is dropped as if the provider sent none, vouched for or not.
package federation

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/users"
)

// Bounds on the claims an adapter hands over (CLAUDE.md rule 2), in bytes.
const (
	MaxProviderLength  = 32
	MaxIssuerLength    = users.MaxIdentityFieldLength
	MaxSubjectLength   = users.MaxIdentityFieldLength
	MaxEmailLength     = users.MaxEmailLength
	MaxNameLength      = users.MaxFullNameLength
	MaxLoginHintLength = 254
)

var (
	// ErrStaffAccount refuses entering or linking a staff account (see
	// Service.refuse).
	ErrStaffAccount = errors.New("a staff account is entered through a provider only from the installation's own directory")
	// ErrSignupClosed means the identity has no account and the installation
	// creates none.
	ErrSignupClosed = errors.New("this installation does not create accounts")
	// ErrInvalidClaims refuses claims that are empty where required, over a
	// bound, or not text, and a trusted email that is not an address.
	ErrInvalidClaims = errors.New("the identity provider's claims are not valid")
	// ErrIdentityConflict refuses linking by email an account already linked
	// to another identity at the same issuer: a provider may reassign a
	// mailbox, and its new holder must not enter the previous one's account.
	ErrIdentityConflict = errors.New("the account is already linked to another identity at this provider")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// users.ErrAccountBlocked and users.ErrAccountDeleted pass through and belong
// to users.
func Errors() []error {
	return []error{ErrStaffAccount, ErrSignupClosed, ErrInvalidClaims, ErrIdentityConflict}
}

// Claims is what the provider's adapter verified about the person.
type Claims struct {
	// Provider is one users.ExternalIdentity knows; Issuer and Subject name
	// the person there and never change.
	Provider, Issuer, Subject string
	Email                     string
	Name                      string
	// LoginHint, usually the person's username at the provider, seeds a new
	// account's login. Name and LoginHint are cleaned and cut to their bounds
	// rather than refused, since a profile's display text is the person's
	// to choose.
	LoginHint string
	// EmailTrusted is the adapter's decision that the provider vouches the
	// person owns Email. Only a trusted email is put on an account or used to
	// find one.
	EmailTrusted bool
	// HomeDirectory is set only by an adapter, for this sign-in; see
	// Service.refuse.
	HomeDirectory bool
}

// Policy is the installation's choice of what federation may do.
type Policy struct {
	// CreateAccounts is SELF_SIGNUP_ENABLED. Off, identities already linked
	// and trusted emails of existing accounts still enter.
	CreateAccounts bool
}

// identity validates the claims and returns the identity they name, its email
// trimmed and lowercased. An untrusted email that is not a bounded address is
// dropped rather than refused: it is never used, so it must not stop a
// sign-in.
func (c Claims) identity() (users.ExternalIdentity, error) {
	for _, f := range []struct {
		name, value string
		max         int
	}{
		{"provider", c.Provider, MaxProviderLength},
		{"issuer", c.Issuer, MaxIssuerLength},
		{"subject", c.Subject, MaxSubjectLength},
	} {
		if !boundedText(f.value, f.max) {
			return users.ExternalIdentity{}, fmt.Errorf("%w: the %s must be at most %d bytes of text", ErrInvalidClaims, f.name, f.max)
		}
	}
	email, ok := address(c.Email)
	switch {
	case !ok && c.EmailTrusted:
		return users.ExternalIdentity{}, fmt.Errorf("%w: the email is not an address of at most %d bytes", ErrInvalidClaims, MaxEmailLength)
	case !ok:
		email = ""
	}
	identity := users.ExternalIdentity{Provider: c.Provider, Issuer: c.Issuer, Subject: c.Subject, Email: email}
	if err := identity.Validate(); err != nil {
		return users.ExternalIdentity{}, fmt.Errorf("%w: %v", ErrInvalidClaims, err)
	}
	return identity, nil
}

// cleaned returns c with Name and LoginHint stripped of invalid UTF-8 and
// control characters and cut to their bounds.
func (c Claims) cleaned() Claims {
	c.Name = cleanText(c.Name, MaxNameLength)
	c.LoginHint = cleanText(c.LoginHint, MaxLoginHintLength)
	return c
}

// cleanText cuts at a character boundary, so the result stays valid UTF-8.
func cleanText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func boundedText(s string, limit int) bool {
	return len(s) <= limit && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// address returns raw trimmed and lowercased, and whether it is empty or a
// bare address of at most MaxEmailLength bytes. A non-ASCII one is returned
// empty.
func address(raw string) (string, bool) {
	if !boundedText(raw, MaxEmailLength) {
		return "", false
	}
	// Checked before lowercasing, which itself folds KELVIN SIGN to k.
	if strings.ContainsFunc(raw, func(r rune) bool { return r > unicode.MaxASCII }) {
		return "", true
	}
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", true
	}
	parsed, err := mail.ParseAddress(email)
	return email, err == nil && parsed.Address == email
}
