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
	"strings"

	"github.com/google/uuid"
)

// Keys.
const (
	KeyName    = "installation.name"
	KeyContact = "installation.contact_email"
	KeyLogo    = "installation.logo"
)

var (
	// ErrUnknownKey refuses a setting nothing reads, so a typo does not look
	// like a saved change.
	ErrUnknownKey = errors.New("no such setting")
	ErrInvalid    = errors.New("setting value is not valid")
)

// maxValueLength bounds a value; values are names and addresses, read on
// every page.
const maxValueLength = 200

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

func anything(value string) error {
	if len(value) > maxValueLength {
		return fmt.Errorf("%w: at most %d characters", ErrInvalid, maxValueLength)
	}
	return nil
}
