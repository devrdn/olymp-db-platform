package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

func TestTheSeededLanguagesAreOffered(t *testing.T) {
	// Three at launch, and a fourth is an INSERT: nothing in the code names
	// them, so this is the only place the seed is asserted.
	withTx(t, func(ctx context.Context) {
		active, err := NewLanguages(testPool).Active(ctx)
		if err != nil {
			t.Fatalf("Active() = %v", err)
		}

		byCode := map[string]string{}
		for _, l := range active {
			byCode[l.Code] = l.NativeName
		}
		for code, native := range map[string]string{"en": "English", "ro": "Română", "ru": "Русский"} {
			if byCode[code] != native {
				t.Errorf("language %q reported as %q, want %q", code, byCode[code], native)
			}
		}
	})
}

func TestARetiredLanguageIsNotOffered(t *testing.T) {
	// Deactivating is how an installation stops offering a language, and it
	// has to be enough on its own.
	withTx(t, func(ctx context.Context) {
		if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`UPDATE languages SET is_active = false WHERE code = 'ru'`); err != nil {
			t.Fatalf("retire a language: %v", err)
		}

		active, err := NewLanguages(testPool).Active(ctx)
		if err != nil {
			t.Fatalf("Active() = %v", err)
		}

		for _, l := range active {
			if l.Code == "ru" {
				t.Error("a retired language is still offered")
			}
		}
	})
}
