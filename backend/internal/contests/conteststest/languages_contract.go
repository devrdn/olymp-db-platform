package conteststest

import (
	"context"
	"slices"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
)

// LanguageTarget is a catalogue holding what a fresh installation holds, and
// the means to change it, which each implementation provides its own way.
type LanguageTarget struct {
	Catalog contests.LanguageCatalog
	// Add offers a further language; l.IsActive says whether it starts active.
	Add    func(l contests.Language)
	Retire func(code string)
}

// LanguageCatalogContract is what every contests.LanguageCatalog must do; both
// the in-memory Languages and postgres.Languages run it. each prepares a fresh
// target for one case, calls run with it, and cleans up.
//
// A fresh target holds the three launch languages, seeded by the migration
// and by NewLanguages; asserting them here fails a seed changed in only one
// place. Cases look only at the codes they name, so an installation's extra
// languages do not disturb them. Codes are lower-case letters because ties in
// display order break by code, and anything else depends on the collation.
func LanguageCatalogContract(t *testing.T, each func(t *testing.T, run func(context.Context, LanguageTarget))) {
	// offered lists, in the order returned, the active languages among codes.
	offered := func(t *testing.T, ctx context.Context, target LanguageTarget, codes ...string) []contests.Language {
		t.Helper()
		active, err := target.Catalog.Active(ctx)
		if err != nil {
			t.Fatalf("Active() = %v", err)
		}
		var found []contests.Language
		for _, l := range active {
			if slices.Contains(codes, l.Code) {
				found = append(found, l)
			}
		}
		return found
	}
	codesOf := func(languages []contests.Language) []string {
		codes := make([]string, 0, len(languages))
		for _, l := range languages {
			codes = append(codes, l.Code)
		}
		return codes
	}

	t.Run("a fresh installation offers English, Romanian and Russian", func(t *testing.T) {
		each(t, func(ctx context.Context, target LanguageTarget) {
			want := []contests.Language{
				{Code: "en", Name: "English", NativeName: "English", IsActive: true, SortOrder: 10},
				{Code: "ro", Name: "Romanian", NativeName: "Română", IsActive: true, SortOrder: 20},
				{Code: "ru", Name: "Russian", NativeName: "Русский", IsActive: true, SortOrder: 30},
			}

			got := offered(t, ctx, target, "en", "ro", "ru")

			if !slices.Equal(got, want) {
				t.Errorf("Active() = %+v, want %+v", got, want)
			}
		})
	})

	t.Run("a language added later is offered with everything it was added with", func(t *testing.T) {
		each(t, func(ctx context.Context, target LanguageTarget) {
			added := contests.Language{Code: "xa", Name: "Xanadu", NativeName: "Ξανάδου", IsActive: true, SortOrder: 40}
			target.Add(added)

			got := offered(t, ctx, target, "xa")

			if !slices.Equal(got, []contests.Language{added}) {
				t.Errorf("Active() = %+v, want just %+v", got, added)
			}
		})
	})

	t.Run("languages come in display order, ties broken by code", func(t *testing.T) {
		each(t, func(ctx context.Context, target LanguageTarget) {
			// Neither insertion order nor code order is the display order.
			target.Add(contests.Language{Code: "xb", Name: "B", NativeName: "B", IsActive: true, SortOrder: 25})
			target.Add(contests.Language{Code: "xa", Name: "A", NativeName: "A", IsActive: true, SortOrder: 25})
			target.Add(contests.Language{Code: "xc", Name: "C", NativeName: "C", IsActive: true, SortOrder: 5})

			got := codesOf(offered(t, ctx, target, "en", "ro", "ru", "xa", "xb", "xc"))

			want := []string{"xc", "en", "ro", "xa", "xb", "ru"}
			if !slices.Equal(got, want) {
				t.Errorf("Active() order = %v, want %v", got, want)
			}
		})
	})

	t.Run("a retired language is not offered and the others still are", func(t *testing.T) {
		each(t, func(ctx context.Context, target LanguageTarget) {
			target.Retire("ro")

			got := codesOf(offered(t, ctx, target, "en", "ro", "ru"))

			if want := []string{"en", "ru"}; !slices.Equal(got, want) {
				t.Errorf("Active() = %v, want %v", got, want)
			}
		})
	})

	t.Run("a language added already retired is not offered", func(t *testing.T) {
		each(t, func(ctx context.Context, target LanguageTarget) {
			target.Add(contests.Language{Code: "xa", Name: "Xanadu", NativeName: "Xanadu", IsActive: false, SortOrder: 1})

			if got := offered(t, ctx, target, "xa"); len(got) != 0 {
				t.Errorf("Active() = %+v, want the retired language left out", got)
			}
		})
	})
}
