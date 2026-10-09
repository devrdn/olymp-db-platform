package contests

import (
	"context"
	"fmt"
)

// Language is one language the installation offers. The set is data in the
// `languages` table, never a constant (§6.2).
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

// checkLanguagesKnown reports an unknown or retired language code. The
// foreign key catches an unknown code only as an opaque violation, and does
// not catch a retired one at all.
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
