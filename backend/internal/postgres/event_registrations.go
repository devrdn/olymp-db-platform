package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventRegistrations stores the event's sign-ups (migration 000051).
type EventRegistrations struct {
	pool *pgxpool.Pool
	uow  *storage.PgxUnitOfWork
}

var _ registrations.Store = (*EventRegistrations)(nil)

func NewEventRegistrations(pool *pgxpool.Pool) *EventRegistrations {
	return &EventRegistrations{pool: pool, uow: storage.NewUnitOfWork(pool)}
}

// eventRegistrationsLock is the advisory lock Insert counts and checks under,
// so two sign-ups at the last place cannot both pass.
const eventRegistrationsLock = 0x45564e54

const eventRegistrationColumns = `e.id, e.first_name, e.last_name, e.email, e.email_key, e.code, e.user_id,
	coalesce(u.login, ''), e.created_at, e.last_sent_at, e.checked_in_at`

func scanEventRegistration(row pgx.Row) (registrations.Registration, error) {
	var r registrations.Registration
	err := row.Scan(&r.ID, &r.FirstName, &r.LastName, &r.Email, &r.EmailKey, &r.Code, &r.UserID,
		&r.Login, &r.CreatedAt, &r.LastSentAt, &r.CheckedInAt)
	return r, err
}

func (r *EventRegistrations) ByEmailKey(ctx context.Context, emailKey string) (registrations.Registration, error) {
	if !storableText(emailKey) {
		return registrations.Registration{}, registrations.ErrNotFound
	}
	found, err := scanEventRegistration(storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT `+eventRegistrationColumns+`
		FROM event_registrations e LEFT JOIN users u ON u.id = e.user_id
		WHERE e.email_key = $1`, emailKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return registrations.Registration{}, registrations.ErrNotFound
	}
	if err != nil {
		return registrations.Registration{}, fmt.Errorf("find the registration: %w", err)
	}
	return found, nil
}

func (r *EventRegistrations) Count(ctx context.Context) (int, error) {
	var n int
	if err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `SELECT count(*) FROM event_registrations`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count the registrations: %w", err)
	}
	return n, nil
}

// Insert decides every refusal under its lock, and a held code is skipped by
// ON CONFLICT rather than raised, so no refusal aborts the caller's
// transaction.
func (r *EventRegistrations) Insert(ctx context.Context, reg registrations.Registration, capacity int, sent bool) (registrations.Registration, error) {
	var stored registrations.Registration
	err := r.uow.Do(ctx, func(ctx context.Context) error {
		q := storage.QuerierFrom(ctx, r.pool)
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1::int, 0)`, eventRegistrationsLock); err != nil {
			return fmt.Errorf("lock the registrations: %w", err)
		}
		var held bool
		var count int
		if err := q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM event_registrations WHERE email_key = $1),
			       (SELECT count(*) FROM event_registrations)`, reg.EmailKey).Scan(&held, &count); err != nil {
			return fmt.Errorf("check the registrations: %w", err)
		}
		if held {
			return registrations.ErrAlreadyRegistered
		}
		if count >= capacity {
			return registrations.ErrFull
		}
		var err error
		stored, err = scanEventRegistration(q.QueryRow(ctx, `
			WITH e AS (
				INSERT INTO event_registrations (first_name, last_name, email, email_key, code, user_id, last_sent_at)
				VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $7::boolean THEN now() END)
				ON CONFLICT DO NOTHING
				RETURNING *
			)
			SELECT `+eventRegistrationColumns+` FROM e LEFT JOIN users u ON u.id = e.user_id`,
			reg.FirstName, reg.LastName, reg.Email, reg.EmailKey, reg.Code, reg.UserID, sent))
		if errors.Is(err, pgx.ErrNoRows) {
			// The email was checked under the lock, so the conflict is the code.
			return registrations.ErrCodeTaken
		}
		if err != nil {
			return fmt.Errorf("store the registration: %w", err)
		}
		return nil
	})
	if err != nil {
		return registrations.Registration{}, err
	}
	return stored, nil
}

func (r *EventRegistrations) AttachAccount(ctx context.Context, emailKey string, userID uuid.UUID) (registrations.Registration, error) {
	attached, err := scanEventRegistration(storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		WITH e AS (
			UPDATE event_registrations SET user_id = $2
			WHERE email_key = $1 AND user_id IS NULL
			RETURNING *
		)
		SELECT `+eventRegistrationColumns+` FROM e LEFT JOIN users u ON u.id = e.user_id`, emailKey, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return registrations.Registration{}, registrations.ErrNotFound
	}
	if err != nil {
		return registrations.Registration{}, fmt.Errorf("attach the account: %w", err)
	}
	return attached, nil
}

func (r *EventRegistrations) ClaimResend(ctx context.Context, id uuid.UUID, cooldown time.Duration) (bool, error) {
	tag, err := storage.QuerierFrom(ctx, r.pool).Exec(ctx, `
		UPDATE event_registrations SET last_sent_at = now()
		WHERE id = $1 AND (last_sent_at IS NULL OR last_sent_at <= now() - make_interval(secs => $2))`,
		id, cooldown.Seconds())
	if err != nil {
		return false, fmt.Errorf("claim the resend: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *EventRegistrations) List(ctx context.Context) ([]registrations.Registration, error) {
	rows, err := storage.QuerierFrom(ctx, r.pool).Query(ctx, `
		SELECT `+eventRegistrationColumns+`
		FROM event_registrations e LEFT JOIN users u ON u.id = e.user_id
		ORDER BY e.created_at DESC, e.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list the registrations: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (registrations.Registration, error) {
		return scanEventRegistration(row)
	})
	if err != nil {
		return nil, fmt.Errorf("list the registrations: %w", err)
	}
	return list, nil
}

func (r *EventRegistrations) SetCheckedIn(ctx context.Context, id uuid.UUID, checkedIn bool) (registrations.Registration, error) {
	updated, err := scanEventRegistration(storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		WITH e AS (
			UPDATE event_registrations
			SET checked_in_at = CASE WHEN $2::boolean THEN coalesce(checked_in_at, now()) END
			WHERE id = $1
			RETURNING *
		)
		SELECT `+eventRegistrationColumns+` FROM e LEFT JOIN users u ON u.id = e.user_id`, id, checkedIn))
	if errors.Is(err, pgx.ErrNoRows) {
		return registrations.Registration{}, registrations.ErrNotFound
	}
	if err != nil {
		return registrations.Registration{}, fmt.Errorf("record the arrival: %w", err)
	}
	return updated, nil
}
