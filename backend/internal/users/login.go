package users

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Login is a login one of the Parse constructors accepted, trimmed and
// checked for the use it was parsed for; the zero Login is empty. Every path
// that makes an account parses its login in Service.checkNewAccount: with
// ParseNewLogin, or with loginFromEmail for a login that is the account's own
// address, which keeps its dots.
type Login struct{ value string }

func (l Login) String() string { return l.value }

// ParseNewLogin trims raw and refuses, with ErrInvalidAccount, a login no new
// account may take: an empty one, one over MaxLoginLength bytes, one holding a
// control or format character, or one holding a dot (IsLabelSeparator), which
// is ErrLoginHasDot. The login is mailed in the sign-in letter, and a mail
// client turns a dotted one into a link to a domain. Letter case is kept;
// existing accounts keep the login they have.
func ParseNewLogin(raw string) (Login, error) {
	login := strings.TrimSpace(raw)
	if err := validateLoginText(login); err != nil {
		return Login{}, err
	}
	if strings.ContainsFunc(login, IsLabelSeparator) {
		return Login{}, ErrLoginHasDot
	}
	return Login{login}, nil
}

// IsLabelSeparator reports whether r splits a domain name into labels: the
// full stop and the three code points UTS #46 maps to it (U+3002, U+FF0E and
// U+FF61), and U+2024 and U+FE52, which UTS #46 disallows but NFKC folds to a
// dot, so a linkifier that normalises reads one.
func IsLabelSeparator(r rune) bool {
	switch r {
	case '.', '\u3002', '\uff0e', '\uff61', '\u2024', '\ufe52':
		return true
	}
	return false
}

// ChosenLoginPattern is the alphabet of a login a person picks on the public
// sign-up form. It holds no dot and no control or format character, so it is
// narrower than ParseNewLogin's rule. The login is mailed to whatever address
// was typed, so it must not carry a pitch.
const ChosenLoginPattern = `^[a-z0-9][a-z0-9_-]{2,63}$`

var chosenLogin = regexp.MustCompile(ChosenLoginPattern)

// ParseChosenLogin trims and lower-cases a self-chosen login and refuses,
// with ErrInvalidAccount, one that is not 3 to 64 lowercase letters, digits,
// hyphens or underscores starting with a letter or a digit.
func ParseChosenLogin(raw string) (Login, error) {
	login := ParseTypedLogin(raw)
	if !chosenLogin.MatchString(login.value) {
		return Login{}, fmt.Errorf("%w: the login must be 3 to 64 lowercase letters, digits, hyphens or underscores, "+
			"starting with a letter or a digit", ErrInvalidAccount)
	}
	return login, nil
}

// ParseTypedLogin is a login typed to sign in, or one already stored, trimmed
// and lower-cased: the form throttle counters and the trusted-device binding
// are keyed by, so case variants share them. It applies no shape rule, so a
// login made before a rule was added keeps signing in. Sign-in looks the
// account up by what was typed, compared as lower(login) in SQL.
func ParseTypedLogin(raw string) Login {
	return Login{TypedKey(raw)}
}

// TypedKey is an identifier a person typed, a login or an address, trimmed
// and lower-cased: what every lookup by what was typed compares and every
// throttle on it is keyed by, so the two count the same string. It applies no
// shape rule and no bound.
func TypedKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// loginFromEmail is the login of an account whose login is its own trimmed
// address, lower-cased (CreateCommand.LoginFromEmail). It keeps its dots: the
// letter goes to that address, so the link a dot makes leads to the reader's
// own mailbox.
func loginFromEmail(email string) (Login, error) {
	if !LowerCaseKeepsWidth(email) {
		return Login{}, fmt.Errorf("%w: the email changes when lower-cased", ErrInvalidAccount)
	}
	login := strings.ToLower(email)
	if err := validateLoginText(login); err != nil {
		return Login{}, err
	}
	return Login{login}, nil
}

// validateLoginText is ParseNewLogin's rule without the dot. A NUL cannot be
// stored, and a format character such as a bidi override reorders the text
// around the login wherever staff read it.
func validateLoginText(login string) error {
	switch {
	case login == "":
		return fmt.Errorf("%w: login must not be empty", ErrInvalidAccount)
	case len(login) > MaxLoginLength:
		return fmt.Errorf("%w: login must be at most %d characters", ErrInvalidAccount, MaxLoginLength)
	case strings.ContainsFunc(login, IsControlOrFormat):
		return fmt.Errorf("%w: login must not contain control or format characters", ErrInvalidAccount)
	}
	return nil
}

// IsControlOrFormat reports whether r is a control or a format character
// (Unicode Cc or Cf), which no new login may hold. IsUnfitForName adds what a
// name or a typed address may not hold either.
func IsControlOrFormat(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }

// LowerCaseKeepsWidth reports whether every letter of s lower-cases to one of
// the same width in bytes. A lower-cased address is a key and a login, and a
// letter that changes width is how a fold to another letter shows: U+0130
// and the Kelvin sign become the ASCII i and k, so the key would name another
// mailbox than the one typed. Letters are checked one by one, so a letter that
// shrinks cannot be hidden by one that grows.
func LowerCaseKeepsWidth(s string) bool {
	for _, r := range s {
		if r >= utf8.RuneSelf && utf8.RuneLen(unicode.ToLower(r)) != utf8.RuneLen(r) {
			return false
		}
	}
	return true
}

// LoginFinder resolves a login (Repository).
type LoginFinder interface {
	// ByLogin may return a deleted account, whose login is free.
	ByLogin(ctx context.Context, login string) (User, error)
}

// RefuseTakenLogin refuses ErrLoginTaken for a login a live account holds,
// in any letter case. It is checked before an account is made, for a clear
// error; the unique index is the real guarantee.
func RefuseTakenLogin(ctx context.Context, logins LoginFinder, login Login) error {
	_, live, err := liveByLogin(ctx, logins, login.value)
	if err != nil {
		return err
	}
	if live {
		return ErrLoginTaken
	}
	return nil
}

// liveByLogin is the live account holding login, in any letter case; false
// when there is none, a deleted account's login being free.
func liveByLogin(ctx context.Context, logins LoginFinder, login string) (User, bool, error) {
	existing, err := logins.ByLogin(ctx, login)
	switch {
	case errors.Is(err, ErrNotFound):
		return User{}, false, nil
	case err != nil:
		return User{}, false, err
	}
	return existing, existing.Status != StatusDeleted, nil
}
