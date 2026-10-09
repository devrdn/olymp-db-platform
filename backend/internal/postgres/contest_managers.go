package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContestManagers implements contests.ManagerRepository.
var _ contests.ManagerRepository = (*ContestManagers)(nil)

// managerColumns joins the account so the staff list shows logins and names.
const managerColumns = `
	m.contest_id, m.user_id, u.login, u.full_name, m.role, m.granted_by, m.granted_at`

// ContestManagers stores who staffs a contest.
type ContestManagers struct {
	pool *pgxpool.Pool
}

// NewContestManagers returns the contest staff repository.
func NewContestManagers(pool *pgxpool.Pool) *ContestManagers {
	return &ContestManagers{pool: pool}
}

func (r *ContestManagers) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

func scanManager(row pgx.Row) (contests.Manager, error) {
	var (
		m    contests.Manager
		role string
	)
	err := row.Scan(&m.ContestID, &m.UserID, &m.Login, &m.FullName, &role, &m.GrantedBy, &m.GrantedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return contests.Manager{}, contests.ErrManagerNotFound
	}
	if err != nil {
		return contests.Manager{}, fmt.Errorf("scan contest manager: %w", err)
	}
	m.Role = rbac.ContestRole(role)
	return m, nil
}

// List returns the contest's staff, owner first.
func (r *ContestManagers) List(ctx context.Context, contestID uuid.UUID) ([]contests.Manager, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT `+managerColumns+`
		FROM contest_managers m
		JOIN users u ON u.id = m.user_id
		WHERE m.contest_id = $1
		-- Owner first whatever their login sorts like: the staff list is read
		-- to find out who is in charge.
		ORDER BY (m.role = 'owner') DESC, u.login`, contestID)
	if err != nil {
		return nil, fmt.Errorf("list contest managers: %w", err)
	}
	defer rows.Close()

	var staff []contests.Manager
	for rows.Next() {
		m, err := scanManager(rows)
		if err != nil {
			return nil, err
		}
		staff = append(staff, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list contest managers: %w", err)
	}
	return staff, nil
}

// Get returns one staff entry.
func (r *ContestManagers) Get(ctx context.Context, contestID, userID uuid.UUID) (contests.Manager, error) {
	return scanManager(r.querier(ctx).QueryRow(ctx, `
		SELECT `+managerColumns+`
		FROM contest_managers m
		JOIN users u ON u.id = m.user_id
		WHERE m.contest_id = $1 AND m.user_id = $2`, contestID, userID))
}

// Grant appoints a user, replacing any role they already held.
func (r *ContestManagers) Grant(ctx context.Context, m contests.Manager) error {
	_, err := r.querier(ctx).Exec(ctx, `
		INSERT INTO contest_managers (contest_id, user_id, role, granted_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (contest_id, user_id) DO UPDATE
		SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by, granted_at = now()`,
		m.ContestID, m.UserID, string(m.Role), m.GrantedBy)
	if err != nil {
		return fmt.Errorf("grant contest role: %w", err)
	}
	return nil
}

// Revoke removes a user from the staff.
func (r *ContestManagers) Revoke(ctx context.Context, contestID, userID uuid.UUID) error {
	tag, err := r.querier(ctx).Exec(ctx,
		`DELETE FROM contest_managers WHERE contest_id = $1 AND user_id = $2`, contestID, userID)
	if err != nil {
		return fmt.Errorf("revoke contest role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return contests.ErrManagerNotFound
	}
	return nil
}
