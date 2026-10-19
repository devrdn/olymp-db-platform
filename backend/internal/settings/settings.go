// Package settings answers what this installation calls itself and what it
// looks like, changed from a screen rather than a deploy.
//
// It does not hold what the process needs to start or anything secret; those
// stay in the environment (platform/config). A wrong value here makes the
// installation look wrong, not unreachable or unsafe. Nor does it hold
// colours: they are design-system tokens with guaranteed contrast.
package settings

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Keys.
const (
	KeyName    = "installation.name"
	KeyContact = "installation.contact_email"
	KeyLogo    = "installation.logo"
	// KeyFeedback is where participants send feedback; empty shows no link.
	KeyFeedback = "installation.feedback_url"
)

var (
	// ErrUnknownKey refuses a setting nothing reads, so a typo does not look
	// like a saved change.
	ErrUnknownKey = errors.New("no such setting")
	ErrInvalid    = errors.New("setting value is not valid")
	// ErrInvalidFeedbackURL has its own code: "not valid" alone would not say
	// that only an absolute https address is taken.
	ErrInvalidFeedbackURL = errors.New("the feedback address is not an absolute https address")
)

// maxValueLength bounds a value; values are names and addresses, read on
// every page.
const maxValueLength = 200

// maxFeedbackURLLength is longer than maxValueLength because form services
// put a long identifier in the path (a Microsoft Forms address runs to about
// two hundred characters).
const maxFeedbackURLLength = 500

// Definition describes one setting the product knows about. The catalogue is
// code and the values are data, so every stored key is one something reads.
type Definition struct {
	Key string
	// Fallback is used until somebody sets a value.
	Fallback string
	// Public marks a setting a page with no session may read (the sign-in
	// screen shows the name and logo). It is an allow-list so that a secret
	// added later is never published by accident.
	Public   bool
	Validate func(string) error
}

// Catalogue is every setting this installation has.
var Catalogue = []Definition{
	{Key: KeyName, Fallback: "DB Contest", Public: true, Validate: required},
	{Key: KeyContact, Public: true, Validate: optionalEmail},
	// The stored value is a reference to an uploaded file, not the file.
	{Key: KeyLogo, Public: true, Validate: anything},
	// Public because the play screen and the profile show it, and the public
	// read is the only one a participant may make.
	{Key: KeyFeedback, Public: true, Validate: optionalFeedbackURL},
}

func Definitions() map[string]Definition {
	byKey := make(map[string]Definition, len(Catalogue))
	for _, d := range Catalogue {
		byKey[d.Key] = d
	}
	return byKey
}

type Values map[string]string

// Repository stores the values; the catalogue is not its business.
type Repository interface {
	// All returns every stored value, whatever keys are in the table.
	All(ctx context.Context) (Values, error)
	Save(ctx context.Context, actorID uuid.UUID, values Values) error
}

func required(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: it must not be empty", ErrInvalid)
	}
	return anything(value)
}

func optionalEmail(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := mail.ParseAddress(value); err != nil {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalid, value)
	}
	return anything(value)
}

// optionalFeedbackURL takes an absolute https address with a host, or nothing.
// The value becomes a link on every participant's screen, so the scheme is an
// allow-list: it refuses javascript: and data:, and refuses plain http, which
// anybody on a school network could rewrite into a page of their own. User
// information is refused so that "https://olympdb.usm.md@evil.example" cannot
// pass for this installation.
func optionalFeedbackURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxFeedbackURLLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidFeedbackURL, maxFeedbackURLLength)
	}
	// url.Parse tolerates a space in a path, and a browser reads a backslash as
	// a slash where Go does not, so the address checked would not be the one
	// followed.
	if strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\\' }) {
		return fmt.Errorf("%w: it contains a space, a backslash or a control character", ErrInvalidFeedbackURL)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" {
		return fmt.Errorf("%w: %q", ErrInvalidFeedbackURL, value)
	}
	return nil
}

func anything(value string) error {
	if len(value) > maxValueLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalid, maxValueLength)
	}
	return nil
}
