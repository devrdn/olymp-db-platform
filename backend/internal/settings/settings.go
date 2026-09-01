// Package settings answers "what does this installation call itself, and what
// does it look like" — the decisions an organisation makes about its own copy
// of the product, changed from a screen rather than from a deploy.
//
// It deliberately does not hold what the process cannot start without, nor
// anything secret: the database DSN, the cache address, the trusted proxies
// and the cookie policy stay in the environment (platform/config). The line is
// drawn by consequence rather than by type — a wrong value here makes the
// installation look wrong, a wrong value there makes it unreachable or unsafe.
//
// Nor does it hold the palette. Colours are tokens of the design system, and
// "let the administrator pick colours" turns a system whose contrast is
// guaranteed into a set of fields where it is not (SPEC 3.3).
package settings

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
)

// Keys. Constants rather than literals so a rename is a compile error and the
// set is discoverable.
const (
	KeyName    = "installation.name"
	KeyContact = "installation.contact_email"
	KeyLogo    = "installation.logo"
)

// Errors the domain reports.
var (
	// ErrUnknownKey refuses a setting nothing reads. A store that accepted
	// anything would fill with keys whose meaning died with whoever typed
	// them, and a typo would look like a saved change.
	ErrUnknownKey = errors.New("no such setting")
	ErrInvalid    = errors.New("setting value is not valid")
)

// maxValueLength bounds what a field may hold. These are names and addresses,
// not documents, and the values are read on every page.
const maxValueLength = 200

// Definition describes one setting the product knows about.
//
// The catalogue is code, while the values are data. That is the right way
// round: a setting nothing reads is not configuration, it is a row somebody
// has to guess the meaning of later.
type Definition struct {
	Key string
	// Fallback is used until somebody sets a value, so a fresh installation
	// renders as something rather than as blanks.
	Fallback string
	// Public marks a setting a page with no session may read.
	//
	// The sign-in screen carries the installation's name and logo, and it is
	// seen before anybody is signed in — so some of this has to be readable by
	// anyone. That makes an allow-list the only safe shape: the day somebody
	// adds a mail server's password here, an endpoint that returned the whole
	// table would publish it, and nothing in that change would look like a
	// disclosure.
	Public bool
	// Validate refuses a value the interface would not survive.
	Validate func(string) error
}

// Catalogue is every setting this installation has.
var Catalogue = []Definition{
	{Key: KeyName, Fallback: "DB Contest", Public: true, Validate: required},
	{Key: KeyContact, Public: true, Validate: optionalEmail},
	// The stored value is a reference to an uploaded file, not the file. What
	// serving it safely requires is the HTTP layer's problem.
	{Key: KeyLogo, Public: true, Validate: anything},
}

// Definitions indexes the catalogue by key.
func Definitions() map[string]Definition {
	byKey := make(map[string]Definition, len(Catalogue))
	for _, d := range Catalogue {
		byKey[d.Key] = d
	}
	return byKey
}

// Values is a set of settings and what they hold.
type Values map[string]string

// Repository stores the values. The catalogue is not its business.
type Repository interface {
	// All returns every stored value, whatever keys happen to be in the table.
	All(ctx context.Context) (Values, error)
	// Save writes these values and stamps who did it.
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

func anything(value string) error {
	if len(value) > maxValueLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalid, maxValueLength)
	}
	return nil
}
