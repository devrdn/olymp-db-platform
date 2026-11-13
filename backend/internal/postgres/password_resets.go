package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PasswordResets stores the reset links waiting to be followed (migration
// 000056).
type PasswordResets struct {
	pool *pgxpool.Pool
}

var _ recovery.Store = (*PasswordResets)(nil)

func NewPasswordResets(pool *pgxpool.Pool) *PasswordResets {
	return &PasswordResets{pool: pool}
}

func (r *PasswordResets) Replace(ctx context.Context, l recovery.Link) error {
	_, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, `
		INSERT INTO password_resets (token_hash, user_id, email, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash, email = EXCLUDED.email,
			expires_at = EXCLUDED.expires_at, created_at = now()`,
		l.TokenHash, l.UserID, l.Email, l.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store the reset link: %w", err)
	}
	return nil
}

func (r *PasswordResets) Find(ctx context.Context, tokenHash []byte, now time.Time) (bool, error) {
	var found bool
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM password_resets WHERE token_hash = $1 AND expires_at > $2)`,
		tokenHash, now).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("find the reset link: %w", err)
	}
	return found, nil
}

// Spend deletes the row: a second spender's DELETE waits on the first's row
// lock and then finds nothing.
func (r *PasswordResets) Spend(ctx context.Context, tokenHash []byte) (recovery.Link, bool, error) {
	l := recovery.Link{TokenHash: tokenHash}
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx,
		`DELETE FROM password_resets WHERE token_hash = $1 RETURNING user_id, email, expires_at`, tokenHash).
		Scan(&l.UserID, &l.Email, &l.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return recovery.Link{}, false, nil
	}
	if err != nil {
		return recovery.Link{}, false, fmt.Errorf("spend the reset link: %w", err)
	}
	return l, true, nil
}

// DeleteFor deletes the links of the accounts.
func (r *PasswordResets) DeleteFor(ctx context.Context, ids []uuid.UUID) error {
	if _, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, `DELETE FROM password_resets WHERE user_id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("delete the reset links: %w", err)
	}
	return nil
}

func (r *PasswordResets) Sweep(ctx context.Context, now time.Time) (int64, error) {
	tag, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, `DELETE FROM password_resets WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("sweep the expired reset links: %w", err)
	}
	return tag.RowsAffected(), nil
}
