// Package postgres implements the repository interfaces the domain packages
// declare, against the core PostgreSQL database. All core SQL lives here.
// It holds no business rules: what a write means is decided by the domain,
// and this package only maps its constraints and SQLSTATEs onto domain errors.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// userColumns is the projection scanUser reads; every account query uses it.
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
	), '{}'),
	COALESCE(a.login, '')`

// userJoin resolves the login of the account in status_changed_by. It is a
// LEFT join because that column is NULL for an account whose status never
// changed.
const userJoin = `LEFT JOIN users a ON a.id = u.status_changed_by`

// usersSearchMatch is the text half of usersSearchWhere, kept separate so a
// test can EXPLAIN it on its own.
//
// Each ILIKE reads a bare column so the trigram indexes from migration
// 000016 apply; a COALESCE around email would not match its index and, being
// OR'd with the others, would force a sequential scan. A NULL email yields
// NULL, which WHERE treats as false, so the matched rows are the same.
const usersSearchMatch = `(u.login ILIKE '%' || $1 || '%'
	            OR u.full_name ILIKE '%' || $1 || '%'
	            OR u.email ILIKE '%' || $1 || '%')`

// usersSearchWhere is the predicate List and Search share: a login, full name
// or email containing $1, restricted to status $2. An empty status means
// every account except deleted ones.
const usersSearchWhere = `
	WHERE ($1 = '' OR ` + usersSearchMatch + `)
	  AND (CASE WHEN $2 = '' THEN u.status <> 'deleted' ELSE u.status = $2 END)`

var _ users.Repository = (*Users)(nil)

var _ contests.UserDirectory = (*Users)(nil)

// Users stores accounts in PostgreSQL.
type Users struct {
	pool *pgxpool.Pool
}

// NewUsers returns the account repository.
func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

// querier returns the ambient transaction when one is open.
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
		&u.StatusChangedByLogin,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return users.User{}, users.ErrNotFound
	}
	if err != nil {
		return users.User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// ByLogin resolves an account case-insensitively, preferring a live account
// over a deleted one.
//
// The unique index on lower(login) is partial (status <> 'deleted'), so a
// reused login has a live row and one or more deleted ones. The ORDER BY puts
// the live row first, then the most recently deleted, then id for a
// deterministic tie. With nothing live it still returns a deleted row, not
// ErrNotFound: auth.Service.Login uses it to tell a former owner the account
// is inaccessible, and callers that must treat it as absent check Status.
//
// A login no account can hold (too long, or text PostgreSQL cannot store) is
// not found without a query, since the statement would fail and abort an
// enclosing roster import.
func (r *Users) ByLogin(ctx context.Context, login string) (users.User, error) {
	if !storableText(login) || len(login) > users.MaxLoginLength {
		return users.User{}, users.ErrNotFound
	}
	row := r.querier(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u `+userJoin+`
		 WHERE lower(u.login) = lower($1)
		 ORDER BY (u.status = 'deleted'), u.status_changed_at DESC, u.id DESC
		 LIMIT 1`, login)
	return scanUser(row)
}

// ByID resolves an account by identifier.
func (r *Users) ByID(ctx context.Context, id uuid.UUID) (users.User, error) {
	row := r.querier(ctx).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u `+userJoin+` WHERE u.id = $1`, id)
	return scanUser(row)
}

// ByIDs resolves the accounts that exist among the ids. A repeated id returns
// its account once, and a missing id is absent rather than an error.
func (r *Users) ByIDs(ctx context.Context, ids []uuid.UUID) ([]users.User, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+userColumns+` FROM users u `+userJoin+` WHERE u.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}
	defer rows.Close()

	var found []users.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, u)
	}
	return found, rows.Err()
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
		SELECT `+userColumns+` FROM inserted u `+userJoin,
		u.Login, email, u.PasswordHash, u.FullName, u.Status, u.MustChangePassword)

	created, err := scanUser(row)
	if err != nil {
		// The unique index, not the service's pre-check, is what catches two
		// concurrent creations of one login.
		return users.User{}, mapUserConstraint(err)
	}
	return created, nil
}

// List returns a page of accounts together with the total number of matches.
func (r *Users) List(ctx context.Context, f users.Filter) ([]users.User, int, error) {
	f = f.Normalize()
	q := r.querier(ctx)
	needle := escapeLike(f.Query)

	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM users u`+usersSearchWhere, needle, f.Status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	rows, err := q.Query(ctx,
		`SELECT `+userColumns+` FROM users u `+userJoin+usersSearchWhere+` ORDER BY u.login LIMIT $3 OFFSET $4`,
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

// Search resolves active accounts by a substring of their login, full name or
// email, for the contest staff and participant pickers.
//
// It runs on every keystroke, so it skips List's count and userColumns'
// role and permission subqueries: the returned users carry only ID, Login,
// FullName and Email. Blocked accounts are excluded because they cannot sign
// in, so appointing or enrolling one would do nothing.
func (r *Users) Search(ctx context.Context, query string, limit int) ([]users.User, error) {
	f := users.Filter{Query: query, Status: users.StatusActive, Limit: limit}.Normalize()
	needle := escapeLike(f.Query)

	rows, err := r.querier(ctx).Query(ctx,
		`SELECT u.id, u.login, u.full_name, COALESCE(u.email, '') FROM users u`+usersSearchWhere+` ORDER BY u.login LIMIT $3`,
		needle, f.Status, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer rows.Close()

	var found []users.User
	for rows.Next() {
		var u users.User
		if err := rows.Scan(&u.ID, &u.Login, &u.FullName, &u.Email); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	return found, nil
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

// SetStatus moves the named accounts to the status. A missing id is skipped,
// not reported: the caller resolves the accounts first and reports its own
// skips.
func (r *Users) SetStatus(ctx context.Context, ids []uuid.UUID, status string, change users.StatusChange) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users
		SET status = $2, status_reason = $3, status_changed_at = $4,
		    status_changed_by = $5, updated_at = now()
		WHERE id = ANY($1)`,
		// A nil By means the system made the change: write NULL, since uuid.Nil
		// would violate the foreign key on status_changed_by.
		ids, status, nullIfEmpty(change.Reason), change.At, nilUUID(change.By))
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

// SetPasswordMany stores a digest per account and marks each one as carrying
// a one-time password, in one statement rather than one per account.
func (r *Users) SetPasswordMany(ctx context.Context, creds []users.Credential) error {
	if len(creds) == 0 {
		return nil
	}

	ids := make([]uuid.UUID, len(creds))
	hashes := make([]string, len(creds))
	seen := make(map[uuid.UUID]struct{}, len(creds))
	for i, c := range creds {
		if _, dup := seen[c.UserID]; dup {
			// Two credentials for one account may carry different hashes, and
			// UPDATE ... FROM would apply an unspecified one, so refuse.
			return fmt.Errorf("set passwords: account %s is named more than once", c.UserID)
		}
		seen[c.UserID] = struct{}{}
		ids[i], hashes[i] = c.UserID, c.Hash
	}
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users u
		SET password_hash = c.hash, must_change_password = true,
		    password_changed_at = now(), updated_at = now()
		FROM unnest($1::uuid[], $2::text[]) AS c(id, hash)
		WHERE u.id = c.id`, ids, hashes)
	if err != nil {
		return fmt.Errorf("set passwords: %w", err)
	}
	return nil
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

// BumpSessionGenerationMany retires every session of every named account. A
// missing id is skipped, as in SetStatus.
func (r *Users) BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users SET session_generation = session_generation + 1, updated_at = now()
		WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("retire sessions: %w", err)
	}
	return nil
}

// RecordLogin stamps a successful sign-in.
func (r *Users) RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
	return r.exec(ctx, `UPDATE users SET last_login_at = $2 WHERE id = $1`, id, at)
}

// CountActiveWithRole returns how many active accounts hold the role. Blocked
// accounts do not count, so the last usable administrator cannot be removed.
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

// TakenAmong returns the deleted accounts among ids whose login or email a
// live account now holds. A bulk restore checks it up front, so one collision
// does not abort the whole transaction.
func (r *Users) TakenAmong(ctx context.Context, ids []uuid.UUID) ([]users.TakenConflict, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT id, login_taken, email_taken FROM (
			SELECT d.id AS id,
				EXISTS (
					SELECT 1 FROM users a
					WHERE a.status <> 'deleted' AND lower(a.login) = lower(d.login)
				) AS login_taken,
				EXISTS (
					SELECT 1 FROM users a
					WHERE a.status <> 'deleted' AND a.email IS NOT NULL AND a.email = d.email
				) AS email_taken
			FROM users d
			WHERE d.id = ANY($1) AND d.status = 'deleted'
		) conflicts
		WHERE login_taken OR email_taken`, ids)
	if err != nil {
		return nil, fmt.Errorf("check restore conflicts: %w", err)
	}
	defer rows.Close()

	var taken []users.TakenConflict
	for rows.Next() {
		var c users.TakenConflict
		if err := rows.Scan(&c.ID, &c.Login, &c.Email); err != nil {
			return nil, fmt.Errorf("scan restore conflict: %w", err)
		}
		taken = append(taken, c)
	}
	return taken, rows.Err()
}

// Roles lists the installation's global roles, ordered by code.
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

// ReplaceRoles sets the account's global roles to these codes. It deletes then
// inserts; the caller's transaction hides the gap.
func (r *Users) ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error {
	q := r.querier(ctx)

	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	if len(roleCodes) == 0 {
		return nil
	}

	// The join drops unknown codes; the row count turns that into an error.
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

// ReplaceRolesMany sets the same roles on every named account in one delete
// and one insert.
func (r *Users) ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error {
	q := r.querier(ctx)

	// A repeated id would make the CROSS JOIN trip the user_roles primary key.
	ids = distinctUserIDs(ids)

	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	// A repeated code would otherwise be reported as unknown by the row count.
	roleCodes = distinctRoleCodes(roleCodes)
	if len(roleCodes) == 0 {
		return nil
	}

	tag, err := q.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT u.id, r.id
		FROM unnest($1::uuid[]) AS u(id)
		CROSS JOIN roles r
		WHERE r.code = ANY($2)`, ids, roleCodes)
	if err != nil {
		return fmt.Errorf("assign roles: %w", err)
	}
	// The join drops unknown codes, so every account should have gained every
	// code. Each matched role adds one row per account, so the division is
	// exact.
	if want := len(ids) * len(roleCodes); int(tag.RowsAffected()) != want {
		matched := 0
		if len(ids) > 0 {
			matched = int(tag.RowsAffected()) / len(ids)
		}
		return fmt.Errorf("assign roles: %d of %d codes are not known roles",
			len(roleCodes)-matched, len(roleCodes))
	}
	return nil
}

// distinctRoleCodes drops repeats while keeping order.
func distinctRoleCodes(codes []string) []string {
	seen := make(map[string]struct{}, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

// distinctUserIDs drops repeats while keeping order.
func distinctUserIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// mapUserConstraint translates a unique violation into the sentinel of the
// violated index, login or email. Anything else passes through unchanged.
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
