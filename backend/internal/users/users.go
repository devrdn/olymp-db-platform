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
)

// Errors the service reports to its callers.
var (
	ErrNotFound      = errors.New("user not found")
	ErrLoginTaken    = errors.New("login already in use")
	ErrEmailTaken    = errors.New("email already in use")
	ErrWeakPassword  = errors.New("password does not meet the policy")
	ErrSamePassword  = errors.New("new password must differ from the current one")
	ErrWrongPassword = errors.New("current password is incorrect")
)

// User is an account.
type User struct {
	ID       uuid.UUID
	Login    string
	Email    string
	FullName string
	Status   string
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

// Repository is the storage the service needs.
type Repository interface {
	// ByLogin resolves an account by its login, case-insensitively. It returns
	// ErrNotFound when there is no such account.
	ByLogin(ctx context.Context, login string) (User, error)
	ByID(ctx context.Context, id uuid.UUID) (User, error)
	// Create stores a new account and returns it with its generated id.
	Create(ctx context.Context, u User) (User, error)
	// List returns a page of accounts ordered by login.
	List(ctx context.Context, f Filter) ([]User, int, error)
	// UpdateProfile changes the mutable descriptive fields.
	UpdateProfile(ctx context.Context, id uuid.UUID, fullName, email string) error
	// SetStatus blocks or unblocks an account.
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
	// SetPassword stores a new digest and clears the one-time-password flag.
	SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	// BumpSessionGeneration retires every session issued for the account and
	// returns the new value.
	BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error)
	// RecordLogin stamps the successful login time.
	RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	// ReplaceRoles sets the account's global roles to exactly these codes.
	ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error
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
