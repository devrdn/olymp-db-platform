package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLPolicies implements contests.PolicyStore.
var _ contests.PolicyStore = (*SQLPolicies)(nil)

// SQLPolicies stores how much SQL power each contest hands out.
type SQLPolicies struct {
	pool *pgxpool.Pool
}

// NewSQLPolicies returns the SQL policy store.
func NewSQLPolicies(pool *pgxpool.Pool) *SQLPolicies {
	return &SQLPolicies{pool: pool}
}

func (r *SQLPolicies) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// ByContest returns the contest's policy.
//
// A contest that was never configured reports the read-only default rather
// than an error: the absence of a policy row must never read as "no
// restrictions".
func (r *SQLPolicies) ByContest(ctx context.Context, contestID uuid.UUID) (contests.SQLPolicy, error) {
	var p contests.SQLPolicy
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT contest_id, mode, writable_tables, allow_create_view, allow_own_tables,
		       allow_temp_tables, allow_catalog, disk_quota_ratio, updated_by, updated_at
		FROM contest_sql_policies WHERE contest_id = $1`, contestID).
		Scan(&p.ContestID, &p.Mode, &p.WritableTables, &p.AllowCreateView, &p.AllowOwnTables,
			&p.AllowTempTables, &p.AllowCatalog, &p.DiskQuotaRatio, &p.UpdatedBy, &p.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return contests.DefaultSQLPolicy(contestID), nil
	}
	if err != nil {
		return contests.SQLPolicy{}, fmt.Errorf("load sql policy: %w", err)
	}
	return p, nil
}

// Save stores the policy.
func (r *SQLPolicies) Save(ctx context.Context, p contests.SQLPolicy) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_sql_policies (contest_id, mode, writable_tables, allow_create_view,
		                                  allow_own_tables, allow_temp_tables, allow_catalog,
		                                  disk_quota_ratio, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (contest_id) DO UPDATE
		SET mode = EXCLUDED.mode,
		    writable_tables = EXCLUDED.writable_tables,
		    allow_create_view = EXCLUDED.allow_create_view,
		    allow_own_tables = EXCLUDED.allow_own_tables,
		    allow_temp_tables = EXCLUDED.allow_temp_tables,
		    allow_catalog = EXCLUDED.allow_catalog,
		    disk_quota_ratio = EXCLUDED.disk_quota_ratio,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = now()`,
		p.ContestID, p.Mode, stringList(p.WritableTables), p.AllowCreateView,
		p.AllowOwnTables, p.AllowTempTables, p.AllowCatalog, p.DiskQuotaRatio, p.UpdatedBy)
	if err != nil {
		return fmt.Errorf("save sql policy: %w", err)
	}
	return nil
}
