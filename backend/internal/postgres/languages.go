package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Languages implements contests.LanguageCatalog.
var _ contests.LanguageCatalog = (*Languages)(nil)

// Languages reads the languages an installation offers.
//
// It is a table read rather than a constant precisely so that adding a fourth
// language is an INSERT: nothing here enumerates codes.
type Languages struct {
	pool *pgxpool.Pool
}

// NewLanguages returns the language catalog.
func NewLanguages(pool *pgxpool.Pool) *Languages {
	return &Languages{pool: pool}
}

// Active returns the languages a contest may currently be authored in.
func (r *Languages) Active(ctx context.Context) ([]contests.Language, error) {
	rows, err := storage.QuerierFrom(ctx, r.pool).Query(ctx, `
		SELECT code, name, native_name, is_active, sort_order
		FROM languages WHERE is_active ORDER BY sort_order, code`)
	if err != nil {
		return nil, fmt.Errorf("list languages: %w", err)
	}
	defer rows.Close()

	var found []contests.Language
	for rows.Next() {
		var l contests.Language
		if err := rows.Scan(&l.Code, &l.Name, &l.NativeName, &l.IsActive, &l.SortOrder); err != nil {
			return nil, fmt.Errorf("scan language: %w", err)
		}
		found = append(found, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list languages: %w", err)
	}
	return found, nil
}
