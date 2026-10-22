package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MailOutbox is the mail_outbox table: the outbox's queue and the
// dispatcher's store.
type MailOutbox struct{ pool *pgxpool.Pool }

var (
	_ mailing.Queue = (*MailOutbox)(nil)
	_ mailing.Store = (*MailOutbox)(nil)
)

// NewMailOutbox returns the repository over pool.
func NewMailOutbox(pool *pgxpool.Pool) *MailOutbox { return &MailOutbox{pool: pool} }

func (r *MailOutbox) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

// mailOutboxLockKey is the advisory lock every replica's claim takes before
// counting the last minute's claims; the core database's single-key locks
// are a sequence, and scheduleLockKey is the one before it.
const mailOutboxLockKey = 8_531_204_477_119_003_3

// Insert queues m, due at once, inside the transaction ctx carries.
func (r *MailOutbox) Insert(ctx context.Context, m mailing.Message) error {
	if _, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO mail_outbox (kind, user_id, recipient, subject, body)
		VALUES ($1, $2, $3, $4, $5)`,
		string(m.Kind), nilUUID(m.UserID), m.To, m.Subject, m.Body); err != nil {
		return fmt.Errorf("insert into the mail outbox: %w", err)
	}
	return nil
}

// Claim leases the oldest due message for lease and counts the attempt. The
// count of the last minute's claims and the claim itself run under one
// advisory lock, or two replicas could each see budget left and both spend
// it; a replica that finds the lock taken claims nothing this time. FOR
// UPDATE SKIP LOCKED passes over a row another session is finishing.
func (r *MailOutbox) Claim(ctx context.Context, perMinute int, lease time.Duration) (mailing.Queued, bool, error) {
	var q mailing.Queued
	var claimed bool
	err := storage.NewUnitOfWork(r.pool).Do(ctx, func(ctx context.Context) error {
		var held bool
		if err := r.querier(ctx).QueryRow(ctx,
			`SELECT pg_try_advisory_xact_lock($1)`, int64(mailOutboxLockKey)).Scan(&held); err != nil {
			return fmt.Errorf("take the mail claim lock: %w", err)
		}
		if !held {
			return nil
		}

		var kind string
		var user *uuid.UUID
		err := r.querier(ctx).QueryRow(ctx, `
			UPDATE mail_outbox
			SET attempts        = attempts + 1,
			    last_attempt_at = now(),
			    next_attempt_at = now() + $2::interval
			WHERE id = (
				SELECT id FROM mail_outbox
				WHERE sent_at IS NULL AND abandoned_at IS NULL
				  AND next_attempt_at <= now()
				  AND (SELECT count(*) FROM mail_outbox
				       WHERE last_attempt_at > now() - interval '1 minute') < $1
				ORDER BY next_attempt_at, id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			)
			RETURNING id, kind, user_id, recipient, subject, body, attempts, created_at`,
			perMinute, lease,
		).Scan(&q.ID, &kind, &user, &q.To, &q.Subject, &q.Body, &q.Attempts, &q.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim a message: %w", err)
		}
		q.Kind = mailing.Kind(kind)
		if user != nil {
			q.UserID = *user
		}
		claimed = true
		return nil
	})
	if err != nil {
		return mailing.Queued{}, false, err
	}
	return q, claimed, nil
}

// MarkSent records the delivery and erases the body.
func (r *MailOutbox) MarkSent(ctx context.Context, id int64) error {
	return r.finish(ctx, id, `
		UPDATE mail_outbox SET sent_at = now(), body = NULL
		WHERE id = $1 AND sent_at IS NULL AND abandoned_at IS NULL`)
}

// Abandon gives the message up with its reason and erases the body.
func (r *MailOutbox) Abandon(ctx context.Context, id int64, lastError string) error {
	return r.finish(ctx, id, `
		UPDATE mail_outbox SET abandoned_at = now(), body = NULL, last_error = $2
		WHERE id = $1 AND sent_at IS NULL AND abandoned_at IS NULL`, lastError)
}

// Retry makes the message due again after the given wait, with its reason.
func (r *MailOutbox) Retry(ctx context.Context, id int64, after time.Duration, lastError string) error {
	return r.finish(ctx, id, `
		UPDATE mail_outbox SET next_attempt_at = now() + $2::interval, last_error = $3
		WHERE id = $1 AND sent_at IS NULL AND abandoned_at IS NULL`, after, lastError)
}

// Release makes the message due again after the given wait and refunds the
// attempt its claim counted.
func (r *MailOutbox) Release(ctx context.Context, id int64, after time.Duration, lastError string) error {
	return r.finish(ctx, id, `
		UPDATE mail_outbox
		SET attempts = attempts - 1, next_attempt_at = now() + $2::interval, last_error = $3
		WHERE id = $1 AND sent_at IS NULL AND abandoned_at IS NULL AND attempts > 0`, after, lastError)
}

// Expire abandons, with reason, every pending message queued more than
// maxAge ago, except one claimed within lease, which a sender may still be
// handing to the relay; its claim's own check abandons it instead.
func (r *MailOutbox) Expire(ctx context.Context, maxAge, lease time.Duration, reason string) (map[mailing.Kind]int, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		WITH expired AS (
			UPDATE mail_outbox SET abandoned_at = now(), body = NULL, last_error = $3
			WHERE sent_at IS NULL AND abandoned_at IS NULL
			  AND created_at < now() - $1::interval
			  AND (last_attempt_at IS NULL OR last_attempt_at <= now() - $2::interval)
			RETURNING kind
		)
		SELECT kind, count(*) FROM expired GROUP BY kind`, maxAge, lease, reason)
	if err != nil {
		return nil, fmt.Errorf("expire old messages: %w", err)
	}
	expired := map[mailing.Kind]int{}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("expire old messages: %w", err)
		}
		expired[mailing.Kind(kind)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("expire old messages: %w", err)
	}
	return expired, nil
}

// finish runs one update of a pending message. Matching nothing means another
// sender finished it after the lease ran out.
func (r *MailOutbox) finish(ctx context.Context, id int64, statement string, args ...any) error {
	tag, err := r.querier(ctx).Exec(ctx, statement, append([]any{id}, args...)...)
	if err != nil {
		return fmt.Errorf("record the outcome of message %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("record the outcome of message %d: it is no longer pending", id)
	}
	return nil
}

// CountDue counts pending messages whose time has come; a leased one is not
// due until its lease ends.
func (r *MailOutbox) CountDue(ctx context.Context) (int, error) {
	var n int
	if err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*) FROM mail_outbox
		WHERE sent_at IS NULL AND abandoned_at IS NULL AND next_attempt_at <= now()`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count due messages: %w", err)
	}
	return n, nil
}
