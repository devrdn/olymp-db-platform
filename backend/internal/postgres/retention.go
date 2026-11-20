package postgres

import (
	"context"
	"fmt"
	"time"

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

// retentionLockKey is the advisory lock every replica's sweep competes for.
// Advisory locks are scoped to a database, so it cannot collide with
// scheduleLockKey or gamedb's prepareLock.
const retentionLockKey = 8_531_204_477_119_004_1

// retentionUnlockTimeout bounds the release, which runs after the sweep's own
// context may already have ended.
const retentionUnlockTimeout = 5 * time.Second

// TryLock takes the sweep's advisory lock on a connection of its own, so the
// sweep's batches run on other connections and outside any transaction, which
// the audit delete requires. It never blocks: a replica that loses gets
// held == false. release must be called once when held; a connection that
// cannot confirm the unlock is closed, which frees the lock.
func (r *Retention) TryLock(ctx context.Context) (release func(), held bool, err error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire a connection for the retention lock: %w", err)
	}
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(retentionLockKey)).Scan(&held); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("acquire the retention lock: %w", err)
	}
	if !held {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), retentionUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, int64(retentionLockKey)).Scan(&unlocked); err != nil || !unlocked {
			_ = conn.Hijack().Close(ctx)
			return
		}
		conn.Release()
	}, true, nil
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
// oldest first. Call it outside any transaction: the append-only exemption
// lasts until the enclosing transaction ends, and UnitOfWork.Do joins an outer one.
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
