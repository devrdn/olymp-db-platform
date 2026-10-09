// Package users owns accounts: who exists, what global roles they hold, and
// the operations an administrator performs on them. It declares the storage it
// needs and never imports a database driver. Signing in belongs to auth.
package users

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Account statuses.
const (
	StatusActive  = "active"
	StatusBlocked = "blocked"
	// StatusDeleted keeps the row so results and the audit trail keep their
	// subject; the account cannot sign in, is not counted as an administrator,
	// and releases its login.
	StatusDeleted = "deleted"
)

// Bounds on an account's descriptive fields (CLAUDE.md rule 2). The email
// bound is the longest address the mail RFCs allow.
const (
	MaxLoginLength    = 100
	MaxFullNameLength = 200
	MaxEmailLength    = 254
)

// MaxStatusReasonLength bounds the explanation stored with a status
// (CLAUDE.md rule 2).
const MaxStatusReasonLength = 500

var (
	ErrNotFound = errors.New("user not found")
	// ErrInvalidAccount reports an empty login or name, a field over its
	// bound, or an invalid email.
	ErrInvalidAccount = errors.New("account details are not valid")
	ErrLoginTaken     = errors.New("login already in use")
	ErrEmailTaken     = errors.New("email already in use")
	ErrWeakPassword   = errors.New("password does not meet the policy")
	ErrSamePassword   = errors.New("new password must differ from the current one")
	ErrWrongPassword  = errors.New("current password is incorrect")
	// ErrLastAdministrator refuses a change that would leave nobody able to
	// manage accounts. Recovery would need hand-written SQL: bootstrap does
	// not touch an existing login's roles.
	ErrLastAdministrator = errors.New("this would leave the installation without an administrator")
	// ErrReasonRequired refuses a block or delete without a reason for the
	// trail.
	ErrReasonRequired = errors.New("a reason is required")
	// ErrAccountDeleted refuses a single-account profile edit, password
	// reset or role change on a deleted account; bulk operations skip it
	// instead (SkipDeleted).
	ErrAccountDeleted = errors.New("this account is deleted")
	// ErrAccountBlocked refuses giving a blocked account something it would
	// have to sign in to use, such as a contest staff role. Distinct from
	// auth.ErrAccountBlocked, which answers the account's own sign-in.
	ErrAccountBlocked = errors.New("this account is blocked")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// Errors passed through from the password hasher or the repository belong to
// those packages.
func Errors() []error {
	return []error{
		ErrNotFound, ErrInvalidAccount, ErrLoginTaken, ErrEmailTaken,
		ErrWeakPassword, ErrSamePassword, ErrWrongPassword,
		ErrLastAdministrator, ErrReasonRequired, ErrAccountDeleted, ErrAccountBlocked,
		ErrCannotActOnSelf, ErrRosterTooLarge, ErrTooManyAccounts,
	}
}

// Statuses is every account status, for validating a filter. The column's
// CHECK constraint remains the real guarantee.
var Statuses = []string{StatusActive, StatusBlocked, StatusDeleted}

type User struct {
	ID       uuid.UUID
	Login    string
	Email    string
	FullName string
	Status   string
	// StatusReason is the administrator's explanation of the current status.
	StatusReason    string
	StatusChangedAt *time.Time
	StatusChangedBy *uuid.UUID
	// StatusChangedByLogin is StatusChangedBy's login, resolved in the same
	// query; empty when StatusChangedBy is nil.
	StatusChangedByLogin string
	// PasswordHash never leaves the server.
	PasswordHash string
	// SessionGeneration retires sessions issued before its current value.
	SessionGeneration  int64
	MustChangePassword bool
	PasswordChangedAt  *time.Time
	LastLoginAt        *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Roles              []string
	// Permissions are loaded with the account, so authentication costs one
	// query.
	Permissions []string
}

func (u User) IsActive() bool { return u.Status == StatusActive }

// Has reports whether the account's roles grant a permission. For the request's
// own caller, rbac.Identity.Has answers the same question.
func (u User) Has(permission string) bool {
	return slices.Contains(u.Permissions, permission)
}

// StatusChange is why, by whom and when a status changed. It travels with the
// new status so storage cannot record one without the other.
type StatusChange struct {
	Reason string
	By     uuid.UUID
	At     time.Time
}

type Repository interface {
	// ByLogin resolves a login case-insensitively, preferring a live account
	// over a deleted one with the same login. It may return a deleted
	// account; a caller that must not count that as found checks Status.
	// ErrNotFound only when nothing matches.
	ByLogin(ctx context.Context, login string) (User, error)
	ByID(ctx context.Context, id uuid.UUID) (User, error)
	// ByIDs resolves the accounts that exist among the ids, in no order; a
	// missing id is simply absent.
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]User, error)
	Create(ctx context.Context, u User) (User, error)
	List(ctx context.Context, f Filter) ([]User, int, error)
	UpdateProfile(ctx context.Context, id uuid.UUID, fullName, email string) error
	// SetStatus moves every named account to the status. The single-account
	// path passes a slice of one, so the two cannot drift.
	SetStatus(ctx context.Context, ids []uuid.UUID, status string, change StatusChange) error
	// SetPassword stores a new digest and sets the one-time-password flag to
	// mustChange.
	SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	// SetPasswordMany stores a digest per account, each marked one-time.
	SetPasswordMany(ctx context.Context, creds []Credential) error
	// BumpSessionGeneration retires every session of the account.
	BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error)
	BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error
	RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error
	ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error
	// TakenAmong returns the deleted accounts whose login or email a live
	// account now holds, so a move out of "deleted" is refused before the
	// transaction, naming the field that collides.
	TakenAmong(ctx context.Context, ids []uuid.UUID) ([]TakenConflict, error)
	// CountActiveWithRole counts accounts holding the role that can still
	// sign in: a blocked administrator cannot administer.
	CountActiveWithRole(ctx context.Context, roleCode string) (int, error)
}

type Credential struct {
	UserID uuid.UUID
	Hash   string
}

// TakenConflict is one deleted account TakenAmong found reclaimed. Login and
// Email say which constraint it would trip; either or both may be true.
type TakenConflict struct {
	ID    uuid.UUID
	Login bool
	Email bool
}

// Role is one of the installation's global roles. Roles come from the
// `roles` table, not constants, so adding one is data.
type Role struct {
	Code string
	Name string
}

type Filter struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

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
// trail. A new field is recorded only if listed here; the digest, the session
// generation and the password timestamps never are.
func (u User) auditFields() map[string]any {
	return map[string]any{
		"full_name": u.FullName,
		"email":     u.Email,
	}
}
