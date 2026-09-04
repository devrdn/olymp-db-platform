// Package users owns accounts: who exists, what global roles they hold, and
// the operations an administrator performs on them.
//
// The package defines the storage interfaces it needs and never imports a
// database driver. Implementations live in internal/postgres, so the business
// rules here are exercised in tests without a database.
package users

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Account statuses.
const (
	StatusActive  = "active"
	StatusBlocked = "blocked"
	// StatusDeleted is an account an administrator has removed. The row stays
	// so results and the audit trail keep their subject; the account cannot
	// sign in, does not count as an administrator, and no longer holds its
	// login.
	StatusDeleted = "deleted"
)

// Bounds on what an account's descriptive fields may hold.
//
// The columns are unbounded text, and the request body is bounded at a
// megabyte, so without these a login could be a megabyte long — indexed,
// compared case-insensitively on every sign-in, and printed in every audit
// line about the account. The numbers are generous for what they name: a
// student card number or a username, a person's name, and the longest address
// the mail RFCs allow.
const (
	MaxLoginLength    = 100
	MaxFullNameLength = 200
	MaxEmailLength    = 254
)

// MaxStatusReasonLength bounds the explanation stored with a status.
//
// The column is unbounded text and the request body is bounded at a
// megabyte, so without this a block reason could be a megabyte read by every
// administrator who opens the account.
const MaxStatusReasonLength = 500

// Errors the service reports to its callers.
var (
	ErrNotFound = errors.New("user not found")
	// ErrInvalidAccount reports descriptive fields the account cannot be
	// created or updated with: an empty login or name, a field over its bound,
	// an email that is not one. Callers answer it as a bad request; an
	// undeclared error here used to reach the client as a 500.
	ErrInvalidAccount = errors.New("account details are not valid")
	ErrLoginTaken     = errors.New("login already in use")
	ErrEmailTaken     = errors.New("email already in use")
	ErrWeakPassword   = errors.New("password does not meet the policy")
	ErrSamePassword   = errors.New("new password must differ from the current one")
	ErrWrongPassword  = errors.New("current password is incorrect")
	// ErrLastAdministrator refuses the change that would leave the
	// installation with nobody able to manage accounts.
	//
	// It is a lockout, not a permission problem: `bootstrap` returns early for
	// a login that already exists and never looks at what roles it still
	// holds, so recovery is hand-written SQL against production.
	ErrLastAdministrator = errors.New("this would leave the installation without an administrator")
	// ErrReasonRequired refuses a status change nobody accounted for. Blocking
	// and deleting are answered to afterwards, and "no reason given" is not an
	// answer the trail can carry.
	ErrReasonRequired = errors.New("a reason is required")
	// ErrAccountDeleted refuses a single-account operation that assumes the
	// account can still be reached — a profile edit, a password reset, a
	// role change — on one that is deleted. Their bulk counterparts
	// (BulkReplaceRoles, BulkResetPassword) already skip a deleted account
	// for the same reason (SkipDeleted); this is what a single-account
	// caller sees instead of a silent no-op or a change nobody can use.
	ErrAccountDeleted = errors.New("this account is deleted")
	// ErrAccountBlocked refuses a single-account operation that assumes the
	// account can be used for what it is about to be given — appointing it to
	// a contest's staff, say — on one that is blocked. Distinct from
	// auth.ErrAccountBlocked, which auth.Service.Login answers with after a
	// correct password: that one reports a sign-in attempt by the account
	// itself, this one a change somebody else is making about it, and the two
	// must not be confused for one another in a stack trace or a test
	// failure. A blocked account cannot sign in either way, so granting it
	// anything is exactly the ErrAccountDeleted case above with a status that
	// still leaves the row reachable for everything else an administrator
	// does with it.
	ErrAccountBlocked = errors.New("this account is blocked")
)

// Statuses is every state an account can be in, for validating a filter
// against something other than a comment. It mirrors the CHECK constraint on
// the column, which remains the real guarantee.
var Statuses = []string{StatusActive, StatusBlocked, StatusDeleted}

// User is an account.
type User struct {
	ID       uuid.UUID
	Login    string
	Email    string
	FullName string
	Status   string
	// StatusReason is why the account is in its current status, as the
	// administrator who put it there wrote it. Empty for an account nobody has
	// blocked or deleted.
	StatusReason    string
	StatusChangedAt *time.Time
	StatusChangedBy *uuid.UUID
	// StatusChangedByLogin is the login of the account named by
	// StatusChangedBy, resolved by the repository's own query rather than a
	// second lookup: the account card needs a name for the actor, and a soft
	// delete never removes the row, so the join always has one to find. Empty
	// when StatusChangedBy is nil.
	StatusChangedByLogin string
	// PasswordHash is the argon2id digest. It never leaves the server and is
	// stripped from anything the API returns.
	PasswordHash string
	// SessionGeneration retires sessions issued before its current value.
	SessionGeneration int64
	// MustChangePassword marks an account still on its one-time password.
	MustChangePassword bool
	PasswordChangedAt  *time.Time
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// Roles are the codes of the global roles the account holds.
	Roles []string
	// Permissions are the codes those roles grant, loaded with the account so
	// the authentication path costs one query, not two.
	Permissions []string
}

// IsActive reports whether the account may authenticate.
func (u User) IsActive() bool { return u.Status == StatusActive }

// StatusChange is the account of a status change: why, by whom, when.
//
// It travels with the new status rather than beside it so that storage cannot
// record a status without recording what explains it.
type StatusChange struct {
	Reason string
	By     uuid.UUID
	At     time.Time
}

// Repository is the storage the service needs.
type Repository interface {
	// ByLogin resolves an account by its login, case-insensitively, preferring
	// a live account over a deleted one when both share the login — deletion
	// releases a login precisely so it can be reused, so a deleted row and its
	// login's new owner can coexist, and the live one must always win. It
	// returns ErrNotFound only when nothing at all matches; when the login
	// belongs to a deleted account and nothing has reclaimed it, that account
	// is still what comes back, and a caller for whom that must not count as
	// "found" (a duplicate-login check, an idempotency check) has to look at
	// Status itself.
	ByLogin(ctx context.Context, login string) (User, error)
	ByID(ctx context.Context, id uuid.UUID) (User, error)
	// ByIDs resolves the accounts that exist among the ids, in no particular
	// order. An id with no account is absent from the result rather than an
	// error: which of them exist is what the caller asked.
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]User, error)
	// Create stores a new account and returns it with its generated id.
	Create(ctx context.Context, u User) (User, error)
	// List returns a page of accounts ordered by login.
	List(ctx context.Context, f Filter) ([]User, int, error)
	// UpdateProfile changes the mutable descriptive fields.
	UpdateProfile(ctx context.Context, id uuid.UUID, fullName, email string) error
	// SetStatus moves every named account to the status, recording what
	// explains the move. One method rather than a single and a batch one: the
	// single-account path passes a slice of one, so the two cannot drift.
	SetStatus(ctx context.Context, ids []uuid.UUID, status string, change StatusChange) error
	// SetPassword stores a new digest and clears the one-time-password flag.
	SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	// SetPasswordMany stores a digest per account and marks each one as
	// carrying a one-time password.
	SetPasswordMany(ctx context.Context, creds []Credential) error
	// BumpSessionGeneration retires every session issued for the account and
	// returns the new value.
	BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error)
	// BumpSessionGenerationMany retires every session of every named account.
	BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error
	// RecordLogin stamps the successful login time.
	RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	// ReplaceRoles sets the account's global roles to exactly these codes.
	ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error
	// ReplaceRolesMany sets the same roles on every named account.
	ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error
	// TakenAmong returns the deleted accounts whose login or email a live
	// account now holds, so a move that would collide — a restore to active,
	// or any other move that leaves "deleted" — is refused before the
	// transaction rather than by it. Each result says which constraint the
	// account would hit, so the caller can report the field that actually
	// collided instead of guessing.
	TakenAmong(ctx context.Context, ids []uuid.UUID) ([]TakenConflict, error)
	// CountActiveWithRole returns how many accounts hold the role and can
	// still sign in.
	//
	// "Active" is half the question: a blocked administrator is an
	// administrator who cannot administer, so counting them would let the last
	// usable one be demoted on the strength of an account nobody can use.
	CountActiveWithRole(ctx context.Context, roleCode string) (int, error)
}

// Credential is one account's new password digest.
type Credential struct {
	UserID uuid.UUID
	Hash   string
}

// TakenConflict is one deleted account TakenAmong found reclaimed by a live
// one. Login and Email say which constraint it would trip if moved off
// "deleted"; either or both may be true.
type TakenConflict struct {
	ID    uuid.UUID
	Login bool
	Email bool
}

// Role is one of the installation's global roles, as a person reads it.
//
// The code is what the authorisation model works in; the name is what an
// administrator picks from a list. Both come from the `roles` table rather
// than from a constant, because adding a role is meant to be data — a
// hard-coded list in any client quietly takes that back.
type Role struct {
	Code string
	Name string
}

// Filter selects a page of accounts.
type Filter struct {
	// Query matches a substring of the login, full name or email.
	Query  string
	Status string
	Limit  int
	Offset int
}

// Normalize clamps the page size so a client cannot ask for the whole table.
func (f Filter) Normalize() Filter {
	const (
		defaultLimit = 50
		maxLimit     = 200
	)
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}

// auditFields is the part of an account that may be written to the audit
// trail.
//
// Declared beside the type rather than at each place a change is recorded, so
// a column added to User is either listed here deliberately or not recorded at
// all. The digest, the session generation and the password timestamps are
// absent on purpose: the trail is read by administrators and kept for a year.
func (u User) auditFields() map[string]any {
	return map[string]any{
		"full_name": u.FullName,
		"email":     u.Email,
	}
}
