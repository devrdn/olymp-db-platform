// Package postgres implements the storage interfaces the domain packages
// declare. All SQL lives here, so swapping the database means writing another
// package rather than editing business rules.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// uniqueViolation is the SQLSTATE PostgreSQL raises for a unique index.
const uniqueViolation = "23505"

// userColumns is the projection every read shares, so a new column is added in
// one place and the scan order cannot drift between queries.
const userColumns = `
	u.id, u.login, COALESCE(u.email, ''), u.password_hash, u.full_name, u.status,
	COALESCE(u.status_reason, ''), u.status_changed_at, u.status_changed_by,
	u.session_generation, u.must_change_password, u.password_changed_at,
	u.last_login_at, u.created_at, u.updated_at,
	COALESCE(ARRAY(
		SELECT r.code FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = u.id
		ORDER BY r.code
	), '{}'),
	COALESCE(ARRAY(
		SELECT DISTINCT p.code FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = u.id
		ORDER BY p.code
	), '{}')`

// Users implements users.Repository; the assertion fails the build here
// rather than at wiring time if the interface and this type drift apart.
var _ users.Repository = (*Users)(nil)

// Users stores accounts in PostgreSQL.
type Users struct {
	pool *pgxpool.Pool
}

// NewUsers returns the account repository.
func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

// querier returns the ambient transaction when one is open, so a repository
// call joins the caller's unit of work instead of writing outside it.
func (r *Users) querier(ctx context.Context) storage.Querier {
	return storage.QuerierFrom(ctx, r.pool)
}

func scanUser(row pgx.Row) (users.User, error) {
	var u users.User
	err := row.Scan(
		&u.ID, &u.Login, &u.Email, &u.PasswordHash, &u.FullName, &u.Status,
		&u.StatusReason, &u.StatusChangedAt, &u.StatusChangedBy,
		&u.SessionGeneration, &u.MustChangePassword, &u.PasswordChangedAt,
		&u.LastLoginAt, &u.CreatedAt, &u.UpdatedAt, &u.Roles, &u.Permissions,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.User{}, users.ErrNotFound
	}
	if err != nil {
		return users.User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// ByLogin resolves an account case-insensitively, matching the unique index on
// lower(login).
func (r *Users) ByLogin(ctx context.Context, login string) (users.User, error) {
	row := r.querier(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE lower(u.login) = lower($1)`, login)
	return scanUser(row)
}

// ByID resolves an account by identifier.
func (r *Users) ByID(ctx context.Context, id uuid.UUID) (users.User, error) {
	row := r.querier(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.id = $1`, id)
	return scanUser(row)
}

// Create stores a new account.
func (r *Users) Create(ctx context.Context, u users.User) (users.User, error) {
	var email *string
	if u.Email != "" {
		email = &u.Email
	}

	row := r.querier(ctx).QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO users (login, email, password_hash, full_name, status, must_change_password)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING *
		)
		SELECT `+userColumns+` FROM inserted u`,
		u.Login, email, u.PasswordHash, u.FullName, u.Status, u.MustChangePassword)

	created, err := scanUser(row)
	if err != nil {
		// The pre-check in the service is a courtesy; this is the guarantee,
		// and it is what catches two administrators creating the same login at
		// the same moment.
		return users.User{}, mapUserConstraint(err)
	}
	return created, nil
}

// List returns a page of accounts together with the total number of matches.
func (r *Users) List(ctx context.Context, f users.Filter) ([]users.User, int, error) {
	f = f.Normalize()
	q := r.querier(ctx)
	// The pattern is parameterized (no injection possible); escaping is about
	// meaning, not safety: the admin's text must match literally (see like.go).
	needle := escapeLike(f.Query)

	// The filter is passed as parameters, never interpolated: the search box is
	// user input reaching a query.
	//
	// An empty status means the register an administrator reads, which is not
	// "every row": a deleted account appears only when asked for by name.
	const where = `
		WHERE ($1 = '' OR u.login ILIKE '%' || $1 || '%'
		            OR u.full_name ILIKE '%' || $1 || '%'
		            OR COALESCE(u.email, '') ILIKE '%' || $1 || '%')
		  AND (CASE WHEN $2 = '' THEN u.status <> 'deleted' ELSE u.status = $2 END)`

	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users u`+where, needle, f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := q.Query(ctx,
		`SELECT `+userColumns+` FROM users u`+where+` ORDER BY u.login LIMIT $3 OFFSET $4`,
		needle, f.Status, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var found []users.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	return found, total, nil
}

// UpdateProfile changes the descriptive fields.
func (r *Users) UpdateProfile(ctx context.Context, id uuid.UUID, fullName, email string) error {
	var emailValue *string
	if email != "" {
		emailValue = &email
	}
	return r.exec(ctx, `
		UPDATE users SET full_name = $2, email = $3, updated_at = now() WHERE id = $1`,
		id, fullName, emailValue)
}

// SetStatus moves the named accounts to the status.
func (r *Users) SetStatus(ctx context.Context, ids []uuid.UUID, status string, change users.StatusChange) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users
		SET status = $2, status_reason = $3, status_changed_at = $4,
		    status_changed_by = $5, updated_at = now()
		WHERE id = ANY($1)`,
		ids, status, nullIfEmpty(change.Reason), change.At, change.By)
	if err != nil {
		return fmt.Errorf("set account status: %w", mapUserConstraint(err))
	}
	return nil
}

// SetPassword stores a new digest.
func (r *Users) SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error {
	return r.exec(ctx, `
		UPDATE users
		SET password_hash = $2, must_change_password = $3,
		    password_changed_at = now(), updated_at = now()
		WHERE id = $1`, id, hash, mustChange)
}

// BumpSessionGeneration retires every session issued for the account.
func (r *Users) BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error) {
	var generation int64
	err := r.querier(ctx).QueryRow(ctx, `
		UPDATE users
		SET session_generation = session_generation + 1, updated_at = now()
		WHERE id = $1
		RETURNING session_generation`, id).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, users.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("bump session generation: %w", err)
	}
	return generation, nil
}

// RecordLogin stamps a successful sign-in.
func (r *Users) RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	return r.exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, id, at)
}

// CountActiveWithRole returns how many accounts hold the role and can sign in.
//
// The status is part of the question, not a refinement of it: a blocked
// administrator cannot administer, and counting them would let the last usable
// one be removed on the strength of an account nobody can use.
func (r *Users) CountActiveWithRole(ctx context.Context, roleCode string) (int, error) {
	var count int
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT count(*)
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		JOIN users u ON u.id = ur.user_id
		WHERE r.code = $1 AND u.status = $2`, roleCode, users.StatusActive).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count accounts holding %q: %w", roleCode, err)
	}
	return count, nil
}

// Roles lists the installation's global roles, ordered by code so the
// interface renders them the same way twice.
func (r *Users) Roles(ctx context.Context) ([]users.Role, error) {
	rows, err := r.querier(ctx).Query(ctx, `SELECT code, name FROM roles ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()

	catalogue := []users.Role{}
	for rows.Next() {
		var role users.Role
		if err := rows.Scan(&role.Code, &role.Name); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		catalogue = append(catalogue, role)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return catalogue, nil
}

// ReplaceRoles sets the account's global roles to exactly these codes.
//
// Delete-then-insert rather than a diff: the set is tiny, and both statements
// run in the caller's transaction, so no request ever observes the gap.
func (r *Users) ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error {
	q := r.querier(ctx)

	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	if len(roleCodes) == 0 {
		return nil
	}

	// Unknown codes are silently dropped by the join; the count check below
	// turns that into an explicit error rather than a half-applied change.
	tag, err := q.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, r.id FROM roles r WHERE r.code = ANY($2)`, id, roleCodes)
	if err != nil {
		return fmt.Errorf("assign roles: %w", err)
	}
	if int(tag.RowsAffected()) != len(roleCodes) {
		return fmt.Errorf("assign roles: %d of %d codes are not known roles",
			len(roleCodes)-int(tag.RowsAffected()), len(roleCodes))
	}
	return nil
}

// mapUserConstraint translates a unique violation into the sentinel the
// violated constraint means. Which index fired matters: reporting a duplicate
// email as "login already in use" sends the administrator fixing the wrong
// field. Anything unrecognised passes through untouched.
func mapUserConstraint(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		return err
	}
	switch pgErr.ConstraintName {
	case "users_login_key", "users_login_lower_key":
		return users.ErrLoginTaken
	case "users_email_key":
		return users.ErrEmailTaken
	default:
		return err
	}
}

// exec runs a statement that must affect exactly one account.
func (r *Users) exec(ctx context.Context, sql string, args ...any) error {
	tag, err := r.querier(ctx).Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("update user: %w", mapUserConstraint(err))
	}
	if tag.RowsAffected() == 0 {
		return users.ErrNotFound
	}
	return nil
}
