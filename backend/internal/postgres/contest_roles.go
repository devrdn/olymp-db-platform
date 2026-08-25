package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContestRoles implements rbac.ContestRoleLoader.
var _ rbac.ContestRoleLoader = (*ContestRoles)(nil)

// ContestRoles resolves a user's standing in one contest.
type ContestRoles struct {
	pool *pgxpool.Pool
}

// NewContestRoles returns the contest-role loader used by authorisation.
func NewContestRoles(pool *pgxpool.Pool) *ContestRoles {
	return &ContestRoles{pool: pool}
}

// ContestRole reports whether the user owns or manages the contest.
//
// Not being staff on a contest is an ordinary answer, not an error: most
// authorisation checks are for people who legitimately have no role there.
func (r *ContestRoles) ContestRole(ctx context.Context, userID, contestID uuid.UUID) (rbac.ContestRole, error) {
	var role string
	err := storage.QuerierFrom(ctx, r.pool).QueryRow(ctx, `
		SELECT role FROM contest_managers WHERE contest_id = $1 AND user_id = $2`,
		contestID, userID).Scan(&role)

	if errors.Is(err, pgx.ErrNoRows) {
		return rbac.RoleNone, nil
	}
	if err != nil {
		return rbac.RoleNone, fmt.Errorf("load contest role: %w", err)
	}
	return rbac.ContestRole(role), nil
}
