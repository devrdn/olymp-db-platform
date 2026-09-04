// Package postgres implements the storage interfaces the domain packages
// declare. All SQL lives here, so swapping the database means writing another
// package rather than editing business rules.
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
	), '{}'),
	COALESCE(a.login, '')`

// userJoin resolves the login of the account named by status_changed_by, so
// the account card can name the actor without a second request for one
// login. LEFT, not an inner join: an account nobody has ever blocked or
// deleted has NULL there, and it must still come back — the COALESCE above
// turns the resulting NULL login into "", matching how the other
// status-change columns already report "nothing to explain".
const userJoin = `LEFT JOIN users a ON a.id = u.status_changed_by`

// usersSearchMatch is the login-or-name-or-email half of usersSearchWhere,
// pulled out on its own so a test can EXPLAIN it apart from the status
// condition below — see users_test.go's TestSearchPredicateUsesTheTrigramIndexes
// for why the two cannot be judged together.
//
// Every ILIKE reads a bare column, not COALESCE(u.email, "") — migration
// 000016_directory_search_indexes adds trigram indexes on exactly the bare
// login, full_name and email columns, and PostgreSQL will not match a
// plain-column index to a COALESCE(...) expression. Because the three
// conditions are OR'd together, that one non-indexable disjunct used to force
// a sequential scan of the whole table for the clause, so the login and
// full_name trigram indexes went unused as well — on a search every
// contest's staff can now reach, not only the handful of administrators the
// account screen serves.
//
// Dropping the wrapper does not change which rows match: email is a nullable
// column, and `u.email ILIKE '%x%'` on a NULL email evaluates to NULL, which
// a WHERE clause treats exactly like `COALESCE(u.email, "") ILIKE '%x%'`
// evaluating to false — both exclude the row. The wrapper was never needed
// for correctness, only for a habit of never comparing directly against a
// nullable column; here that habit is what made the column unindexable.
const usersSearchMatch = `(u.login ILIKE '%' || $1 || '%'
	            OR u.full_name ILIKE '%' || $1 || '%'
	            OR u.email ILIKE '%' || $1 || '%')`

// usersSearchWhere is the predicate List and Search both filter by: a login,
// full name or email that contains the query, restricted to the requested
// status. List and Search share the literal string rather than each writing
// their own, so the two can never drift apart on what "matches" means.
//
// The empty-query branch ($1 = "") short-circuits usersSearchMatch entirely,
// so an empty query is never limited by what that predicate can or cannot
// index.
const usersSearchWhere = `
	WHERE ($1 = '' OR ` + usersSearchMatch + `)
	  AND (CASE WHEN $2 = '' THEN u.status <> 'deleted' ELSE u.status = $2 END)`

// Users implements users.Repository; the assertion fails the build here
// rather than at wiring time if the interface and this type drift apart.
var _ users.Repository = (*Users)(nil)

// Users also implements contests.UserDirectory: the staff and participant
// pickers resolve candidates through the same repository the account screens
// use, rather than a second copy of the same query.
var _ contests.UserDirectory = (*Users)(nil)

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
// over a deleted one whenever both match.
//
// The partial unique index on lower(login) (WHERE status <> 'deleted') only
// promises at most one *live* row per login — it says nothing about a
// deleted one. Deletion exists precisely so a login can be reused, so once an
// account has been deleted and recreated, two rows legitimately share
// lower(login): the deleted original and the live account that now actually
// means that login. A bare SELECT with no ORDER BY gives no guarantee which
// one a single-row QueryRow gets, and before this ORDER BY existed that let
// sign-in resolve to the deleted original (checking the password against the
// wrong hash) and let Service.Create refuse to recreate the account at all
// (finding the deleted row and reporting the login taken). `status = 'deleted'`
// is false for a live row and true for a deleted one, and false sorts first,
// so ORDER BY puts a live row ahead of a deleted one whenever one exists.
//
// When nothing live matches, this still returns a deleted row rather than
// ErrNotFound — the login is not literally unclaimed, only free to be
// reclaimed. auth.Service.Login relies on that: it is what lets a deleted
// account's own owner be told the account is inaccessible rather than that
// no such login exists (see TestDeletedAccountIsRejectedEvenWithTheRightPassword).
// A caller for whom a deleted account must NOT count as "found" —
// users.Service.Create's duplicate check, BootstrapAdmin's idempotency
// check — has to inspect the returned Status itself; ByLogin only orders the
// candidates, it does not decide who is allowed to treat which one as absent.
//
// A login can go through delete, recreate, delete again, so more than one
// *deleted* row can legitimately share lower(login) too — the live-wins ORDER
// BY above says nothing about which of those wins when nothing live matches.
// `u.status_changed_at DESC` breaks that tie by picking the row deleted most
// recently: it is the one that held the login last, so it is the row whose
// history — who deleted it, and why — an administrator asking "what happened
// to this login" actually wants, and it is also the row auth.Service.Login
// will name in refusing a former owner access. `u.id DESC` is a last resort
// after that, only reached if two rows were deleted in the same instant, to
// keep the result deterministic even then rather than merely likely.
func (r *Users) ByLogin(ctx context.Context, login string) (users.User, error) {
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

// ByIDs resolves the accounts that exist among the ids.
//
// ANY($1) is a membership test against the users table, not a join against
// the array — a repeated id in ids still returns that account once, and a
// missing one is simply absent rather than users.ErrNotFound: telling the
// caller which of its ids exist is the whole job.
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

// Search resolves accounts by a substring of their login, full name or
// email — the picker behind contests.Service.SearchPeople
// (internal/contests's UserDirectory).
//
// It shares List's predicate (usersSearchWhere) but is not implemented as a
// call to List: List always runs a "SELECT count(*)" for its total, and a
// typeahead has no use for one — it shows a handful of matches, never a page
// count. Routing through List would spend a second sequential pass over the
// table computing an answer this method would then throw away, on every
// debounced keystroke a picker sends.
//
// It does not select userColumns either, and for the same reason: that
// projection carries two correlated ARRAY(...) subqueries for roles and
// permissions and a join for the status-changer's login, none of which
// contests.Service.SearchPeople reads — it turns every row into a Person of
// a login, a full name and an email, nothing more. Running those subqueries
// for up to DirectorySearchMaxLimit rows on every keystroke a picker sends
// would be work spent computing an answer nobody asked for, on a search
// every contest's staff can reach. The returned users.User carries only ID,
// Login, FullName and Email; every other field is its zero value, which is
// fine for the one caller this method has.
//
// The email is coalesced to an empty string, same as userColumns above: the
// column is nullable, and contests.Person.Email must come back as "" for an
// account with none, not a value that reads as an address somebody actually
// gave. This COALESCE costs nothing extra on the plan the way the one on
// usersSearchMatch's own predicate did (see that constant's comment) — it
// wraps a selected column, not one being matched by an index, so it neither
// touches the trigram indexes nor adds a join or a subquery: the projection
// widens by one plain column, the query shape stays the same.
//
// The status is pinned to StatusActive rather than left empty. List's own
// empty status means "the register an administrator reads" — every account
// except a deleted one, blocked included, because an administrator has to be
// able to find a blocked account to unblock it. A picker is a different
// question: it offers a candidate to appoint or enrol, and a blocked account
// is exactly as unusable there as a deleted one — auth.Service.Login and
// auth.Middleware both refuse it, so offering it here only lets staff appoint
// or enrol somebody who can never act on it and be told the server succeeded.
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

// SetStatus moves the named accounts to the status.
//
// A missing id is not reported as an error: it is silently skipped instead of
// returning users.ErrNotFound the way the single-row exec helper does. This
// method has no such helper to fall back on because the coming bulk path
// resolves the accounts before writing and reports a missing one as a skip in
// its own result, not as a failure of the write; the caller is the one
// positioned to say which id was missing, this method only knows how many
// rows an UPDATE touched.
func (r *Users) SetStatus(ctx context.Context, ids []uuid.UUID, status string, change users.StatusChange) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users
		SET status = $2, status_reason = $3, status_changed_at = $4,
		    status_changed_by = $5, updated_at = now()
		WHERE id = ANY($1)`,
		// A nil By means the system made the change, not a missing user — write
		// NULL rather than uuid.Nil, or the foreign key on status_changed_by
		// raises a raw constraint violation instead of the actor being absent.
		ids, status, nullIfEmpty(change.Reason), change.At, nullIfEmptyUUID(change.By))
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
			// A repeated id here is not a harmless repeat the way it is for
			// ReplaceRolesMany's roleCodes: two Credentials for the same
			// account can carry different hashes, and unnest's join against
			// UPDATE ... FROM applies an unspecified one of them with no
			// error. Silently picking or deduplicating would hide that the
			// caller lost track of its own selection at the exact moment it
			// matters — which password the account actually ends up with —
			// so this is refused instead.
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
// missing id is skipped rather than reported, matching SetStatus: the bulk
// path resolves accounts before writing and reports a missing one itself.
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

// TakenAmong returns the deleted accounts among ids whose login or email a
// live account now holds, saying which of the two each one hit.
//
// Checked before a bulk move's transaction rather than by it: a move off
// "deleted" that would collide is refused up front, without aborting the
// accounts around it that would have succeeded.
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

// ReplaceRolesMany sets the same roles on every named account, in one
// delete and one insert rather than one pair per account.
func (r *Users) ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error {
	q := r.querier(ctx)

	// Accounts are a set here too, matching roleCodes below: a repeated id is
	// harmless because both copies want the identical set of roles, unlike
	// SetPasswordMany where two copies can disagree on which password wins
	// and are refused instead. Left alone, a repeated id would make the
	// CROSS JOIN insert two identical (user_id, role_id) rows and trip the
	// user_roles primary key instead of being absorbed the way it is here.
	ids = distinctUserIDs(ids)

	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	// Roles are a set an account holds, not a sequence of assignments: a
	// caller-supplied duplicate must not inflate what "one row per code"
	// means below, or a harmless repeat gets reported as an unknown role.
	roleCodes = distinctRoleCodes(roleCodes)
	if len(roleCodes) == 0 {
		return nil
	}

	// The cross join is the batch form of the single-account insert: every
	// named account against every named role, in one statement.
	tag, err := q.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT u.id, r.id
		FROM unnest($1::uuid[]) AS u(id)
		CROSS JOIN roles r
		WHERE r.code = ANY($2)`, ids, roleCodes)
	if err != nil {
		return fmt.Errorf("assign roles: %w", err)
	}
	// Unknown codes are dropped by the join rather than refused by it, so the
	// row count is what turns a typo into an error instead of a silent
	// half-applied change: every account should have gained every code. The
	// row count is always an exact multiple of len(ids) — each matched role
	// contributes one row per account — so the division below is exact.
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

// distinctRoleCodes drops repeats while keeping order, so a caller-supplied
// duplicate cannot be counted as a second, unknown role by the row-count
// check in ReplaceRolesMany.
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

// distinctUserIDs drops repeats while keeping order, the uuid.UUID
// counterpart to distinctRoleCodes: a repeated id must not inflate what "one
// row per account" means in the row-count check that follows it.
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

// nullIfEmptyUUID stores NULL rather than the zero UUID, so a system-initiated
// status change leaves status_changed_by absent instead of tripping its
// foreign key to users.
func nullIfEmptyUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
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
