// Package users owns accounts: who exists, what global roles they hold, and
// the operations an administrator performs on them, including emailing an
// issued one-time password through the outbox it is given. It declares the
// storage it needs and never imports a database driver. Signing in belongs
// to auth, and writing and sending email to mailing.
package users

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// MaxRolesPerAccount bounds the role codes one request carries (CLAUDE.md
// rule 2), well above the installation's roles.
const MaxRolesPerAccount = 16

var (
	ErrNotFound = errors.New("user not found")
	// ErrInvalidAccount reports an empty login or name, a field over its
	// bound, an invalid email, or a listing query over MaxQueryLength.
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
	// ErrUnknownRole names a role code the role catalog does not hold.
	ErrUnknownRole = errors.New("unknown role")
	// ErrExternalIdentityTaken refuses linking an identity an account already
	// holds, as when two first sign-ins of one person race.
	ErrExternalIdentityTaken = errors.New("this external identity is already linked to an account")
	// ErrStaffNotLinkable refuses linking an external identity to an account
	// holding any staff permission.
	ErrStaffNotLinkable = errors.New("a staff account cannot be linked to an external identity")
	// ErrIssuerAlreadyLinked refuses linking an account to a second identity
	// at an issuer it already holds one at.
	ErrIssuerAlreadyLinked = errors.New("the account is already linked to another identity at this issuer")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// Errors passed through from the password hasher or the repository belong to
// those packages.
func Errors() []error {
	return []error{
		ErrNotFound, ErrInvalidAccount, ErrLoginTaken, ErrEmailTaken,
		ErrWeakPassword, ErrSamePassword, ErrWrongPassword,
		ErrLastAdministrator, ErrReasonRequired, ErrAccountDeleted, ErrAccountBlocked,
		ErrCannotActOnSelf, ErrRosterTooLarge, ErrTooManyAccounts, ErrUnknownRole, ErrMailOff,
		ErrExternalIdentityTaken, ErrStaffNotLinkable, ErrIssuerAlreadyLinked,
	}
}

// Statuses is every account status, for validating a filter. The column's
// CHECK constraint remains the real guarantee.
var Statuses = []string{StatusActive, StatusBlocked, StatusDeleted}

type User struct {
	ID    uuid.UUID
	Login string
	Email string
	// EmailVerifiedAt is when the account proved it owns Email; nil until
	// then.
	EmailVerifiedAt *time.Time
	FullName        string
	Status          string
	// StatusReason is the administrator's explanation of the current status.
	StatusReason    string
	StatusChangedAt *time.Time
	StatusChangedBy *uuid.UUID
	// StatusChangedByLogin is StatusChangedBy's login, resolved in the same
	// query; empty when StatusChangedBy is nil.
	StatusChangedByLogin string
	// PasswordHash never leaves the server. It is empty for an account that
	// signs in only through an external provider.
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

// HasPassword reports whether the account can sign in with a password.
func (u User) HasPassword() bool { return u.PasswordHash != "" }

// IsStaff reports whether the account holds any permission. The student role
// holds none.
func (u User) IsStaff() bool { return len(u.Permissions) > 0 }

// ReachableByEmail reports whether a password reset link may be mailed to the
// account and followed: a live, active student with an address. Staff are
// never reached through email, since an address someone typed must not
// become a way into a permission; an administrator resets them.
func (u User) ReachableByEmail() bool {
	return u.IsActive() && !u.IsStaff() && u.Email != ""
}

// HasVerifiedEmailIn reports whether the account has proved an address in the
// domain or one of its subdomains. An unverified address proves nothing.
func (u User) HasVerifiedEmailIn(domain string) bool {
	if u.EmailVerifiedAt == nil || domain == "" {
		return false
	}
	_, host, ok := strings.Cut(strings.ToLower(u.Email), "@")
	domain = strings.ToLower(domain)
	return ok && (host == domain || strings.HasSuffix(host, "."+domain))
}

// IsWrittenAsAName reports whether s holds only what a person's name is
// written with: letters, marks, spaces, hyphens, apostrophes and periods. A
// name greets the reader of mail sent to an address anyone may type, so it
// must not carry a link or a pitch; a period directly followed by a letter is
// refused too, since mail clients turn "evil.com" into a link, while "Jr."
// and "J. R. R." stay. It does not bound the length.
func IsWrittenAsAName(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, notOfAName) && !periodBeforeLetter(s)
}

func notOfAName(r rune) bool {
	switch r {
	case ' ', '-', '\'', '’', '.':
		return false
	}
	return !unicode.IsLetter(r) && !unicode.IsMark(r)
}

func periodBeforeLetter(s string) bool {
	afterPeriod := false
	for _, r := range s {
		if afterPeriod && unicode.IsLetter(r) {
			return true
		}
		afterPeriod = r == '.'
	}
	return false
}

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
	// UpdateProfile clears the email's verified mark unless keepVerified.
	UpdateProfile(ctx context.Context, id uuid.UUID, fullName, email string, keepVerified bool) error
	// SetStatus moves every named account to the status. The single-account
	// path passes a slice of one, so the two cannot drift.
	SetStatus(ctx context.Context, ids []uuid.UUID, status string, change StatusChange) error
	// SetPassword stores a new digest and sets the one-time-password flag to
	// mustChange. An empty digest leaves the account without a password.
	SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error
	// SetPasswordMany stores a digest per account, each marked one-time.
	SetPasswordMany(ctx context.Context, creds []Credential) error
	// BumpSessionGeneration retires every session of the account.
	BumpSessionGeneration(ctx context.Context, id uuid.UUID) (int64, error)
	BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error
	RecordLogin(ctx context.Context, id uuid.UUID, at time.Time) error
	// Roles lists the installation's global roles.
	Roles(ctx context.Context) ([]Role, error)
	ReplaceRoles(ctx context.Context, id uuid.UUID, roleCodes []string) error
	ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error
	// TakenAmong returns the deleted accounts whose login or email a live
	// account now holds, so a move out of "deleted" is refused before the
	// transaction, naming the field that collides.
	TakenAmong(ctx context.Context, ids []uuid.UUID) ([]TakenConflict, error)
	// ByEmail resolves a live account by email, ignoring case; ErrNotFound
	// otherwise, a deleted account included.
	ByEmail(ctx context.Context, email string) (User, error)
	// Lock holds the account's row until the transaction in ctx ends, which
	// ctx must carry, so a change to its status, roles or address (each
	// writes the row) waits for it; ErrNotFound for a missing account.
	Lock(ctx context.Context, id uuid.UUID) error
	// MarkEmailVerified records when the account proved its email;
	// ErrNotFound for a missing account.
	MarkEmailVerified(ctx context.Context, id uuid.UUID, at time.Time) error
	// CountActiveWithRole counts accounts holding the role that can still
	// sign in: a blocked administrator cannot administer.
	CountActiveWithRole(ctx context.Context, roleCode string) (int, error)
}

// Providers an account can sign in through. The column's CHECK constraint
// holds the same list.
const ProviderMicrosoft = "microsoft"

// MaxIdentityFieldLength bounds an external identity's issuer and subject
// (CLAUDE.md rule 2).
const MaxIdentityFieldLength = 255

// ExternalIdentity ties an account to a person's identity at a provider.
// Issuer and Subject together name that person there, so the provider's own
// account can change its email without cutting the link.
type ExternalIdentity struct {
	UserID                    uuid.UUID
	Provider, Issuer, Subject string
	Email                     string
}

// Validate refuses a provider this installation does not know and a field
// that is empty or over its bound, with ErrInvalidAccount.
func (i ExternalIdentity) Validate() error {
	switch {
	case i.Provider != ProviderMicrosoft:
		return fmt.Errorf("%w: unknown identity provider %q", ErrInvalidAccount, i.Provider)
	case i.Issuer == "" || utf8.RuneCountInString(i.Issuer) > MaxIdentityFieldLength:
		return fmt.Errorf("%w: the issuer must be 1 to %d characters", ErrInvalidAccount, MaxIdentityFieldLength)
	case i.Subject == "" || utf8.RuneCountInString(i.Subject) > MaxIdentityFieldLength:
		return fmt.Errorf("%w: the subject must be 1 to %d characters", ErrInvalidAccount, MaxIdentityFieldLength)
	case utf8.RuneCountInString(i.Email) > MaxEmailLength:
		return fmt.Errorf("%w: the email must be at most %d characters", ErrInvalidAccount, MaxEmailLength)
	}
	return nil
}

// ExternalIdentities is the storage of provider identities.
type ExternalIdentities interface {
	// ByExternalIdentity resolves the account an identity is linked to;
	// ErrNotFound when none is.
	ByExternalIdentity(ctx context.Context, provider, issuer, subject string) (User, error)
	// LinkExternalIdentity fails when the identity already belongs to an
	// account, this one included.
	LinkExternalIdentity(ctx context.Context, id ExternalIdentity) error
	// HoldsOtherIdentityAt reports whether the account is linked to an
	// identity at the provider's issuer with a subject other than subject.
	HoldsOtherIdentityAt(ctx context.Context, userID uuid.UUID, provider, issuer, subject string) (bool, error)
	// TouchExternalIdentity stamps a sign-in; a missing identity is
	// ErrNotFound.
	TouchExternalIdentity(ctx context.Context, provider, issuer, subject string, at time.Time) error
	// DeleteExternalIdentities forgets every identity of the account.
	DeleteExternalIdentities(ctx context.Context, userID uuid.UUID) error
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

// MaxQueryLength bounds Filter.Query, in runes (CLAUDE.md rule 2): the text
// runs through three trigram ILIKEs, twice, on every listing.
const MaxQueryLength = 100

type Filter struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

// Validate refuses a Query over MaxQueryLength with ErrInvalidAccount.
// Normalize cannot: it corrects the paging silently, and a cut query would
// list the wrong accounts without saying so.
func (f Filter) Validate() error {
	if utf8.RuneCountInString(f.Query) > MaxQueryLength {
		return fmt.Errorf("%w: the search text must be at most %d characters", ErrInvalidAccount, MaxQueryLength)
	}
	return nil
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
