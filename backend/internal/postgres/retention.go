package postgres

import (
	"context"
	"fmt"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/retention"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Retention deletes expired rows from the journal, the audit trail and the
// mail outbox, one bounded batch per call.
type Retention struct{ pool *pgxpool.Pool }

var _ retention.Store = (*Retention)(nil)

// NewRetention returns the store over pool.
func NewRetention(pool *pgxpool.Pool) *Retention { return &Retention{pool: pool} }

func (r *Retention) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// DeleteQueryLog deletes up to limit journal rows of contests still archived
// that were archived more than archivedMonths ago. A contest that left the
// archive keeps its journal.
func (r *Retention) DeleteQueryLog(ctx context.Context, archivedMonths, limit int) (int, error) {
	tag, err := r.querier(ctx).Exec(ctx, `
		DELETE FROM query_log WHERE id IN (
			SELECT q.id
			FROM contests c
			JOIN query_log q ON q.contest_id = c.id
			WHERE c.status = 'archived'
			  AND c.archived_at < now() - make_interval(months => $1)
			LIMIT $2)`, archivedMonths, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired %s rows: %w", retention.TableQueryLog, err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteAuditLog deletes up to limit audit entries older than olderThanMonths,
// oldest first. The append-only guard opens only for the transaction that
// names itself, and the setting is transaction-local, so it closes with the batch.
func (r *Retention) DeleteAuditLog(ctx context.Context, olderThanMonths, limit int) (int, error) {
	var deleted int
	err := storage.NewUnitOfWork(r.pool).Do(ctx, func(ctx context.Context) error {
		q := r.querier(ctx)
		if _, err := q.Exec(ctx,
			`SELECT set_config('olympdb.audit_retention', pg_current_xact_id()::text, true)`); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `
			DELETE FROM audit_log WHERE id IN (
				SELECT id FROM audit_log
				WHERE created_at < now() - make_interval(months => $1)
				ORDER BY created_at
				LIMIT $2)`, olderThanMonths, limit)
		if err != nil {
			return err
		}
		deleted = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete expired %s rows: %w", retention.TableAuditLog, err)
	}
	return deleted, nil
}

// DeleteMailOutbox deletes up to limit messages sent or abandoned more than
// finishedDays ago. A pending message is the dispatcher's and never matches.
func (r *Retention) DeleteMailOutbox(ctx context.Context, finishedDays, limit int) (int, error) {
	tag, err := r.querier(ctx).Exec(ctx, `
		DELETE FROM mail_outbox WHERE id IN (
			SELECT id FROM mail_outbox
			WHERE coalesce(sent_at, abandoned_at) < now() - make_interval(days => $1)
			  AND (sent_at IS NOT NULL OR abandoned_at IS NOT NULL)
			LIMIT $2)`, finishedDays, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired %s rows: %w", retention.TableMailOutbox, err)
	}
	return int(tag.RowsAffected()), nil
}
