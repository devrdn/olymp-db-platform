package contests

import (
	"context"
	"fmt"
)

// Language is one language the installation offers.
//
// The set is data, never a constant: adding a fourth language is an INSERT
// into `languages`, with no migration, deploy or Go change (see §6.2). Nothing
// in this package enumerates language codes.
type Language struct {
	Code       string
	Name       string
	NativeName string
	IsActive   bool
	SortOrder  int
}

// LanguageCatalog reads the languages an installation offers.
type LanguageCatalog interface {
	// Active returns the languages a contest may currently be authored in, in
	// display order.
	Active(ctx context.Context) ([]Language, error)
}

// checkLanguagesKnown reports an unknown or retired language code.
//
// The foreign key would catch an unknown code too, but only as an opaque
// constraint violation; an organizer who typed "rus" deserves to be told which
// code was wrong, and to be told before anything is written. A retired
// language the key would not catch at all: deactivating one is how an
// installation stops offering it, and a new contest must not pick it up again.
func checkLanguagesKnown(known []Language, requested []ContestLanguage) error {
	active := make(map[string]struct{}, len(known))
	for _, l := range known {
		if l.IsActive {
			active[l.Code] = struct{}{}
		}
	}

	for _, want := range requested {
		if _, ok := active[want.Code]; !ok {
			return fmt.Errorf("%w: %q", ErrUnknownLanguage, want.Code)
		}
	}
	return nil
}
