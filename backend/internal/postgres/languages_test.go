package postgres

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
)

// The shared contract (conteststest.LanguageCatalogContract) also pins the
// migration seed to the in-memory catalogue's three languages.
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
