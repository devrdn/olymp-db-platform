package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/storage"
	"github.com/devrdn/olymp-db-platform/backend/internal/signup"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Signups stores the sign-ups waiting for their code (migration 000055).
type Signups struct {
	pool *pgxpool.Pool
	uow  *storage.PgxUnitOfWork
}

var _ signup.Store = (*Signups)(nil)

func NewSignups(pool *pgxpool.Pool) *Signups {
	return &Signups{pool: pool, uow: storage.NewUnitOfWork(pool)}
}

// signupsLock is the advisory lock Put checks and writes under, so two
// sign-ups cannot both take the last place or one login, and the unique
// indexes never fail a statement.
const signupsLock = 0x5349474e

const signupColumns = `id, email, login, full_name, password_hash, code_hash, token_hash, tries, sent_at, expires_at`

func (r *Signups) Waiting(ctx context.Context, email string, now time.Time) (int, error) {
	var n int
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx,
		`SELECT count(*) FROM pending_signups WHERE expires_at > $2 AND email <> $1`, email, now).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the pending sign-ups: %w", err)
	}
	return n, nil
}

func (r *Signups) LoginHeld(ctx context.Context, login, email string, now time.Time) (bool, error) {
	if !storableText(login) || !storableText(email) {
		return false, nil
	}
	var held bool
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pending_signups
		               WHERE lower(login) = lower($1) AND email <> $2 AND expires_at > $3)`,
		login, email, now).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("check the pending sign-ups' logins: %w", err)
	}
	return held, nil
}

func (r *Signups) Put(ctx context.Context, p signup.Pending, max int, now time.Time) error {
	return r.uow.Do(ctx, func(ctx context.Context) error {
		q := storage.QuerierFrom(ctx, r.pool)
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, 0)`, signupsLock); err != nil {
			return fmt.Errorf("lock the pending sign-ups: %w", err)
		}
		if _, err := q.Exec(ctx, `
			DELETE FROM pending_signups
			WHERE lower(login) = lower($1) AND email <> $2 AND expires_at <= $3`, p.Login, p.Email, now); err != nil {
			return fmt.Errorf("release an expired sign-up's login: %w", err)
		}
		var held bool
		var waiting int
		if err := q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pending_signups WHERE lower(login) = lower($1) AND email <> $2),
			       (SELECT count(*) FROM pending_signups WHERE expires_at > $3 AND email <> $2)`,
			p.Login, p.Email, now).Scan(&held, &waiting); err != nil {
			return fmt.Errorf("check the pending sign-ups: %w", err)
		}
		if held {
			return users.ErrLoginTaken
		}
		if waiting >= max {
			return signup.ErrFull
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO pending_signups (email, login, full_name, password_hash, code_hash, token_hash, sent_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (email) DO UPDATE SET
				login = EXCLUDED.login, full_name = EXCLUDED.full_name,
				password_hash = EXCLUDED.password_hash, code_hash = EXCLUDED.code_hash,
				token_hash = EXCLUDED.token_hash, tries = 0,
				sent_at = EXCLUDED.sent_at, expires_at = EXCLUDED.expires_at, created_at = now()`,
			p.Email, p.Login, p.FullName, p.PasswordHash, p.CodeHash, p.TokenHash, p.SentAt, p.ExpiresAt); err != nil {
			return fmt.Errorf("store the pending sign-up: %w", err)
		}
		return nil
	})
}

func (r *Signups) Take(ctx context.Context, email string) (signup.Pending, bool, error) {
	if !storableText(email) {
		return signup.Pending{}, false, nil
	}
	var p signup.Pending
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx,
		`SELECT `+signupColumns+` FROM pending_signups WHERE email = $1 FOR UPDATE`, email).
		Scan(&p.ID, &p.Email, &p.Login, &p.FullName, &p.PasswordHash, &p.CodeHash, &p.TokenHash, &p.Tries, &p.SentAt, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return signup.Pending{}, false, nil
	}
	if err != nil {
		return signup.Pending{}, false, fmt.Errorf("take the pending sign-up: %w", err)
	}
	return p, true, nil
}

func (r *Signups) CountTry(ctx context.Context, id uuid.UUID) error {
	return r.exec(ctx, "count a try", `UPDATE pending_signups SET tries = tries + 1 WHERE id = $1`, id)
}

func (r *Signups) Renew(ctx context.Context, id uuid.UUID, codeHash []byte, sentAt, expiresAt time.Time) error {
	return r.exec(ctx, "renew the code", `
		UPDATE pending_signups SET code_hash = $2, sent_at = $3, expires_at = $4 WHERE id = $1`,
		id, codeHash, sentAt, expiresAt)
}

func (r *Signups) Delete(ctx context.Context, id uuid.UUID) error {
	return r.exec(ctx, "delete the pending sign-up", `DELETE FROM pending_signups WHERE id = $1`, id)
}

func (r *Signups) Sweep(ctx context.Context, now time.Time) (int64, error) {
	tag, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, `DELETE FROM pending_signups WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("sweep the expired sign-ups: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Signups) exec(ctx context.Context, what, sql string, args ...any) error {
	if _, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
