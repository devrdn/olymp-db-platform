// Package settings answers what this installation calls itself, what it looks
// like and which language its email is written in, changed from a screen
// rather than a deploy.
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
	"regexp"
	"strconv"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/google/uuid"
)

// Keys.
const (
	KeyName    = "installation.name"
	KeyContact = "installation.contact_email"
	KeyLogo    = "installation.logo"
	// KeyFeedback is where participants send feedback; empty shows no link.
	KeyFeedback = "installation.feedback_url"
	// KeyMailLanguage is the language of every email the installation sends.
	KeyMailLanguage = "installation.mail_language"
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
	// Romanian because most of the people an olympiad writes to read it.
	{Key: KeyMailLanguage, Fallback: string(mailing.Romanian), Validate: mailLanguage},
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

// feedbackAuthority is the host and optional port the feedback address may
// name: DNS labels of ASCII letters, digits and hyphens whose last label starts
// with a letter. No IP literal, no userinfo, no brackets: on those Go's parser
// and a browser's disagree, or a browser reads the host as a number.
// frontend/lib/api/settings-terms.ts holds the same expression.
var feedbackAuthority = regexp.MustCompile(`^((?:[A-Za-z0-9-]+\.)*[A-Za-z][A-Za-z0-9-]*)(?::([0-9]{1,5}))?$`)

// optionalFeedbackURL takes an absolute https address, or nothing. The value
// becomes a link on every participant's screen, so the scheme is an
// allow-list: it refuses javascript: and data:, and refuses plain http, which
// anybody on a school network could rewrite into a page of their own.
//
// The rule is written out rather than left to url.Parse, because the
// interface applies it too (feedbackHref) and the two parsers disagree on
// edge cases. Printable ASCII only, so a byte is a character, and a homograph
// or a bidirectional override cannot dress up the host; a punycode label is
// refused for the same reason. The shared cases are in
// testdata/feedback-urls.json.
func optionalFeedbackURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxFeedbackURLLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalidFeedbackURL, maxFeedbackURLLength)
	}
	for i := 0; i < len(value); i++ {
		if c := value[i]; c < 0x21 || c > 0x7e || c == '\\' {
			return fmt.Errorf("%w: only printable ASCII, with no spaces or backslashes", ErrInvalidFeedbackURL)
		}
	}
	const scheme = "https://"
	if !strings.EqualFold(value[:min(len(scheme), len(value))], scheme) ||
		!validFeedbackAuthority(value[len(scheme):]) ||
		!validPercentEscapes(value) {
		return fmt.Errorf("%w: %q", ErrInvalidFeedbackURL, value)
	}
	// What passed above is a URL Go reads the same way; this is a backstop.
	if parsed, err := url.Parse(value); err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("%w: %q", ErrInvalidFeedbackURL, value)
	}
	return nil
}

// validFeedbackAuthority reads the authority at the start of rest, up to the
// first "/", "?" or "#".
func validFeedbackAuthority(rest string) bool {
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	match := feedbackAuthority.FindStringSubmatch(authority)
	if match == nil {
		return false
	}
	for _, label := range strings.Split(match[1], ".") {
		if len(label) >= 4 && strings.EqualFold(label[:4], "xn--") {
			return false
		}
	}
	if match[2] != "" {
		port, err := strconv.Atoi(match[2])
		if err != nil || port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

// validPercentEscapes holds when every "%" starts a two-digit hex escape.
// url.Parse checks the path and fragment but not the query, and a browser
// checks none.
func validPercentEscapes(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] != '%' {
			continue
		}
		if i+2 >= len(value) || !isHex(value[i+1]) || !isHex(value[i+2]) {
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func mailLanguage(value string) error {
	names := make([]string, 0, len(mailing.Languages()))
	for _, lang := range mailing.Languages() {
		if value == string(lang) {
			return nil
		}
		names = append(names, string(lang))
	}
	return fmt.Errorf("%w: the mail language is one of %s", ErrInvalid, strings.Join(names, ", "))
}

func anything(value string) error {
	if len(value) > maxValueLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalid, maxValueLength)
	}
	return nil
}
