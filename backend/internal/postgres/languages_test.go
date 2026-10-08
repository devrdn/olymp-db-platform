package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// What a single caller can observe of the language catalogue is the contract
// every contests.LanguageCatalog answers to, the in-memory one the service
// tests use included (conteststest.LanguageCatalogContract). It also holds the
// seed the migrations insert, which nothing in the code names, to the three
// languages the in-memory catalogue starts with. What the schema itself
// enforces (the foreign keys naming a language) is not part of it, and has no
// test here yet.
func TestLanguagesHonoursTheCatalogContract(t *testing.T) {
	conteststest.LanguageCatalogContract(t, func(t *testing.T, run func(context.Context, conteststest.LanguageTarget)) {
		withTx(t, func(ctx context.Context) {
			run(ctx, conteststest.LanguageTarget{
				Catalog: NewLanguages(testPool),
				Add: func(l contests.Language) {
					if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx, `
						INSERT INTO languages (code, name, native_name, is_active, sort_order)
						VALUES ($1, $2, $3, $4, $5)`,
						l.Code, l.Name, l.NativeName, l.IsActive, l.SortOrder); err != nil {
						t.Fatalf("add language %q: %v", l.Code, err)
					}
				},
				Retire: func(code string) {
					if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
						`UPDATE languages SET is_active = false WHERE code = $1`, code); err != nil {
						t.Fatalf("retire language %q: %v", code, err)
					}
				},
			})
		})
	})
}
