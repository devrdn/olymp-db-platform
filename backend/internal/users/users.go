// Package users owns accounts: who exists, what global roles they hold, what
// a new login may look like, and the operations an administrator performs on
// them, including emailing an issued one-time password through the outbox it
// is given. It declares the storage it needs and never imports a database
// driver. Signing in belongs to auth, and writing and sending email to
// mailing.
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
	// bound or holding a character it may not, a new login with a dot
	// (ErrLoginHasDot), an invalid email, or a listing query over
	// MaxQueryLength.
	ErrInvalidAccount = errors.New("account details are not valid")
	// ErrLoginHasDot refuses a new login holding a dot (ParseNewLogin). It
	// wraps ErrInvalidAccount, so every caller that answers that one still
	// does, and the administrator's form and import can name the cause.
	ErrLoginHasDot   = fmt.Errorf("%w: a new login must not contain a dot", ErrInvalidAccount)
	ErrLoginTaken    = errors.New("login already in use")
	ErrEmailTaken    = errors.New("email already in use")
	ErrWeakPassword  = errors.New("password does not meet the policy")
	ErrSamePassword  = errors.New("new password must differ from the current one")
	ErrWrongPassword = errors.New("current password is incorrect")
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
	// ErrStaffNotLinkable refuses linking an external identity to a staff
	// account (see federation.Service.refuse).
	ErrStaffNotLinkable = errors.New("a staff account is linked to an external identity only from the installation's own directory")
	// ErrUnverifiedNotLinkable refuses linking by email an account that does
	// not act as staff and is neither verified nor on a one-time password
	// (User.LinkableByEmail).
	ErrUnverifiedNotLinkable = errors.New("an account that never proved its email and is not on a one-time password is not linked by email")
	// ErrIssuerAlreadyLinked refuses linking an account to a second identity
	// at an issuer it already holds one at.
	ErrIssuerAlreadyLinked = errors.New("the account is already linked to another identity at this issuer")
)

// Errors lists every sentinel this package declares (CLAUDE.md rule 1).
// Errors passed through from the password hasher or the repository belong to
// those packages.
func Errors() []error {
	return []error{
		ErrNotFound, ErrInvalidAccount, ErrLoginHasDot, ErrLoginTaken, ErrEmailTaken,
		ErrWeakPassword, ErrSamePassword, ErrWrongPassword,
		ErrLastAdministrator, ErrReasonRequired, ErrAccountDeleted, ErrAccountBlocked,
		ErrCannotActOnSelf, ErrRosterTooLarge, ErrTooManyAccounts, ErrUnknownRole, ErrMailOff,
		ErrExternalIdentityTaken, ErrStaffNotLinkable, ErrUnverifiedNotLinkable, ErrIssuerAlreadyLinked,
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

// LinkableByEmail reports whether an address a provider vouches for may carry
// its holder into the account: the account proved the address, or is still on
// the one-time password it was issued, which nobody has chosen yet. Any other
// account may have been made with somebody else's address, as event
// registration with mail off does, and its creator would keep access.
func (u User) LinkableByEmail() bool { return u.EmailVerifiedAt != nil || u.MustChangePassword }

// IsStaff reports whether the account holds any global permission. The
// student role holds none. A contest's manager or owner may hold none either;
// where an address could open the account, ActsAsStaff decides.
func (u User) IsStaff() bool { return len(u.Permissions) > 0 }

// ContestManagers says which accounts run a contest (Repository).
type ContestManagers interface {
	// ManagesContest reports whether the account manages or owns a contest
	// that is not archived; false for an id no account has.
	ManagesContest(ctx context.Context, id uuid.UUID) (bool, error)
}

// ActsAsStaff reports whether u holds a global permission or manages or owns
// a contest that is not archived, which reads its reference answers with no
// global role. It asks contests whatever u holds, so its time tells one
// account from another no more than the read that found u. It costs a query:
// for recovery, sign-up and external sign-in, not for every request.
func ActsAsStaff(ctx context.Context, contests ContestManagers, u User) (bool, error) {
	manages, err := contests.ManagesContest(ctx, u.ID)
	if err != nil {
		return false, fmt.Errorf("check whether the account runs a contest: %w", err)
	}
	return u.IsStaff() || manages, nil
}

// ReachableByEmail reports whether a password reset link may be mailed to the
// account and followed: a live, active account with an address that does not
// act as staff (ActsAsStaff).
func ReachableByEmail(ctx context.Context, contests ContestManagers, u User) (bool, error) {
	staff, err := ActsAsStaff(ctx, contests, u)
	if err != nil {
		return false, err
	}
	return u.IsActive() && !staff && u.Email != "", nil
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

// IsUnfitForName reports whether r may not appear in a typed address, nor in
// a name except as unfitInName allows: a control or format character; a line
// or paragraph separator, which breaks the line in some renderers; or a
// variation selector or another default ignorable code point (the Hangul
// fillers, the combining grapheme joiner, the Khmer inherent vowels), each of
// which shows as nothing, so a name of them is blank and an address with one
// reads as another.
func IsUnfitForName(r rune) bool {
	return IsControlOrFormat(r) ||
		unicode.In(r, unicode.Zl, unicode.Zp, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point)
}

// unfitInName reports whether name[i] may not stand in a name: what
// IsUnfitForName names, except a format character a script needs, in that
// script's own context only:
//   - a zero-width non-joiner or joiner after a letter or mark of a script
//     that spells with one, such as Persian, Hindi or Sinhala, and before a
//     letter or mark (a simpler form of IDNA2008's CONTEXTJ rule);
//   - a zero-width joiner after a nonspacing mark of such a script, whatever
//     follows, as a word-final legacy Malayalam chillu or Bengali khanda ta
//     is written;
//   - a Mongolian free variation selector right after a Mongolian letter,
//     and the vowel separator after one, or after such a selector;
//   - an ideographic variation selector (U+E0100 to U+E01EF) right after a
//     Han letter, as a Japanese family name is written in its registered
//     glyph.
//
// Latin, Cyrillic and Greek need none of them, and a character of the Common
// or Inherited script, such as a combining accent, is no context for one.
func unfitInName(name []rune, i int) bool {
	r := name[i]
	if !IsUnfitForName(r) {
		return false
	}
	if i == 0 {
		return true
	}
	before := name[i-1]
	switch {
	case r == '\u180e' && isMongolianSelector(before) && i >= 2:
		return !isMongolianLetter(name[i-2])
	case isMongolianSelector(r) || r == '\u180e':
		return !isMongolianLetter(before)
	case r >= '\U000E0100' && r <= '\U000E01EF':
		return !unicode.IsLetter(before) || !unicode.Is(unicode.Han, before)
	case r == '\u200c' || r == '\u200d':
		if !isNameLetterOrMark(before) || unicode.In(before, unicode.Latin, unicode.Cyrillic, unicode.Greek, unicode.Common, unicode.Inherited) {
			return true
		}
		if r == '\u200d' && unicode.Is(unicode.Mn, before) {
			return false
		}
		return i == len(name)-1 || !isNameLetterOrMark(name[i+1])
	}
	return true
}

func isMongolianSelector(r rune) bool {
	return r == '\u180b' || r == '\u180c' || r == '\u180d' || r == '\u180f'
}

func isMongolianLetter(r rune) bool { return unicode.IsLetter(r) && unicode.Is(unicode.Mongolian, r) }

func isNameLetterOrMark(r rune) bool {
	return (unicode.IsLetter(r) || unicode.IsMark(r)) && !IsUnfitForName(r)
}

// holdsUnfitInName reports whether a name holds a rune unfitInName refuses.
func holdsUnfitInName(name string) bool {
	runes := []rune(name)
	for i := range runes {
		if unfitInName(runes, i) {
			return true
		}
	}
	return false
}

// StripUnfitFromName returns name without the runes unfitInName refuses, for
// a name its holder cannot correct, such as a provider's display name. A
// joiner it keeps stands between two letters or marks it keeps too.
func StripUnfitFromName(name string) string {
	runes := []rune(name)
	kept := make([]rune, 0, len(runes))
	for i, r := range runes {
		if !unfitInName(runes, i) {
			kept = append(kept, r)
		}
	}
	return string(kept)
}

// showsSomething reports whether s holds a character that is seen: a letter,
// a digit, a punctuation mark or a symbol. A mark alone renders as nothing,
// and so does the braille blank, a symbol that prints as a space.
func showsSomething(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return r != '\u2800' && !IsUnfitForName(r) &&
			(unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r))
	})
}

// IsWrittenAsAName reports whether s holds only what a person's name is
// written with, and at least one letter: letters other than a filler
// (IsUnfitForName), marks, the joiners and selectors unfitInName allows,
// spaces, hyphens, apostrophes and periods. A name greets the reader of mail
// sent to an address anyone may type, so it must not carry a link or a pitch;
// a period directly followed by a letter is refused too, since mail clients
// turn "evil.com" into a link, while "Jr." and "J. R. R." stay. It does not
// bound the length.
func IsWrittenAsAName(s string) bool {
	if !utf8.ValidString(s) || !strings.ContainsFunc(s, isNameLetter) || periodBeforeLetter(s) {
		return false
	}
	runes := []rune(s)
	for i, r := range runes {
		if unfitInName(runes, i) || !ofAWrittenName(r) && !IsUnfitForName(r) {
			return false
		}
	}
	return true
}

func isNameLetter(r rune) bool { return unicode.IsLetter(r) && !IsUnfitForName(r) }

// ofAWrittenName reports whether r is a letter, a mark or one of the
// punctuation a written name holds. A character IsUnfitForName names is of a
// written name exactly where unfitInName allows it.
func ofAWrittenName(r rune) bool {
	switch r {
	case ' ', '-', '\'', '’', '.':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsMark(r)
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
	// ctx must carry, so a write to the row (its status, address, password or
	// session generation) waits for it; a role change waits only at the
	// session generation the service bumps. ErrNotFound for a missing
	// account.
	Lock(ctx context.Context, id uuid.UUID) error
	// MarkEmailVerified records when the account proved its email;
	// ErrNotFound for a missing account.
	MarkEmailVerified(ctx context.Context, id uuid.UUID, at time.Time) error
	// CountActiveWithRole counts accounts holding the role that can still
	// sign in: a blocked administrator cannot administer.
	CountActiveWithRole(ctx context.Context, roleCode string) (int, error)
	ContestManagers
}

// Providers an account can sign in through. The CHECK constraint on
// external_identities.provider holds the same list.
const (
	ProviderMicrosoft = "microsoft"
	ProviderGoogle    = "google"
	ProviderGitHub    = "github"
)

// Providers lists every provider an identity may belong to, in the order the
// sign-in page offers them.
var Providers = []string{ProviderMicrosoft, ProviderGoogle, ProviderGitHub}

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
	case !slices.Contains(Providers, i.Provider):
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

// MaxPageSize is the most accounts one page of a listing carries; Normalize
// clamps a larger limit to it.
const MaxPageSize = 200

func (f Filter) Normalize() Filter {
	const defaultLimit = 50
	if f.Limit <= 0 {
		f.Limit = defaultLimit
	}
	if f.Limit > MaxPageSize {
		f.Limit = MaxPageSize
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
