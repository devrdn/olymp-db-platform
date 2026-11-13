package users

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// Password policy: length only, since composition rules mostly produce
// "Password1!".
const (
	MinPasswordLength = 12

	oneTimePasswordBytes = 12
)

// ErrCannotActOnSelf refuses operations that would lock an administrator out.
var ErrCannotActOnSelf = errors.New("this operation cannot be performed on your own account")

// errUnhandledSkipReason means setStatus does not know a skip reason
// BulkSetStatus returned: a programming error, never a caller's refusal, so
// it is unexported.
var errUnhandledSkipReason = errors.New("users: unhandled skip reason for a single-account operation")

type Service struct {
	repo  Repository
	audit *audit.Recorder
	uow   storage.UnitOfWork
	// passwords serves the account's owner.
	passwords *password.Hasher
	// issuing serves passwords an administrator hands out, with a longer
	// wait.
	issuing *password.Hasher
	// access is nil when nothing caches accounts.
	access AccessCache
	// mail is nil when the composition root wired none (WithMail).
	mail *Mail
	// identities is nil when nothing links external identities.
	identities ExternalIdentities
}

// WithExternalIdentities is for the composition root, before the service is
// shared. Deleting an account then forgets its identities, so the same
// person can sign up again.
func (s *Service) WithExternalIdentities(store ExternalIdentities) *Service {
	s.identities = store
	return s
}

// AccessCache is auth's short-lived copy of accounts. Every operation that
// changes what authentication decides on (status, roles, password, session
// generation) names the changed accounts once the change commits. Forget
// returns nothing: a copy it could not drop expires within seconds.
type AccessCache interface {
	Forget(ctx context.Context, ids []uuid.UUID)
}

// WithAccessCache is for the composition root, before the service is shared.
func (s *Service) WithAccessCache(c AccessCache) *Service {
	s.access = c
	return s
}

// forget tells the access cache the accounts changed. It runs after the unit
// of work returns, when the change has committed; earlier, a request could
// re-cache the old row. This assumes no caller wraps these operations in its
// own transaction. The caller's cancellation is ignored, so a hang-up cannot
// leave the old state cached.
func (s *Service) forget(ctx context.Context, ids ...uuid.UUID) {
	if s.access == nil || len(ids) == 0 {
		return
	}
	s.access.Forget(context.WithoutCancel(ctx), ids)
}

// NewService assembles the account service. Every multi-write operation runs
// inside uow, so an action and its audit entry land together. hasher must be
// the process's one hasher, shared with sign-in, or its limit on concurrent
// hashes, and so on memory, does not hold.
func NewService(repo Repository, recorder *audit.Recorder, uow storage.UnitOfWork, hasher *password.Hasher) *Service {
	if hasher == nil {
		panic("users: NewService needs the shared password hasher")
	}
	return &Service{
		repo: repo, audit: recorder, uow: uow,
		passwords: hasher,
		issuing:   hasher.WithMaxWait(administrativeHashWait),
	}
}

// administrativeHashWait is how long issuing a password waits for a hashing
// slot. Administrators' batches are bounded, and an import abandoned halfway
// would discard one-time passwords already generated, so they wait in the
// shared slots rather than lose to a burst of sign-ins.
const administrativeHashWait = 30 * time.Second

type CreateCommand struct {
	ActorID  uuid.UUID
	Login    string
	Email    string
	FullName string
	Roles    []string
	Delivery Delivery
	// SelfService is an account its owner asked for at a public form: its
	// password waits only the sign-in wait for a hashing slot, so a burst of
	// visitors cannot hold the shared slots for the administrative one.
	SelfService bool
}

type CreateResult struct {
	User User
	// OneTimePassword is returned once and never stored in clear.
	OneTimePassword string
	Email           EmailOutcome
}

// Create registers an account with a generated one-time password, which the
// user must replace at first login.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (CreateResult, error) {
	if err := s.refuseMailOff(cmd.Delivery); err != nil {
		return CreateResult{}, err
	}
	roles, err := s.checkRoles(ctx, cmd.Roles)
	if err != nil {
		return CreateResult{}, err
	}
	cmd.Roles = roles
	letters, err := s.letterheadFor(ctx, cmd.Delivery)
	if err != nil {
		return CreateResult{}, err
	}
	return s.create(ctx, cmd, letters)
}

// create is Create for roles checkRoles has already passed, with the
// letterhead read when email was asked for.
func (s *Service) create(ctx context.Context, cmd CreateCommand, letters *letterhead) (CreateResult, error) {
	login, fullName, email := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName), strings.TrimSpace(cmd.Email)
	if err := ValidateAccount(login, fullName, email); err != nil {
		return CreateResult{}, err
	}

	if err := s.refuseTakenLogin(ctx, login); err != nil {
		return CreateResult{}, err
	}

	oneTime, err := generatePassword()
	if err != nil {
		return CreateResult{}, err
	}
	// Written before the hash and the transaction, so a message that cannot
	// be written costs neither.
	message, emailed, err := letters.signIn(User{Login: login, FullName: fullName, Email: email, Status: StatusActive}, oneTime)
	if err != nil {
		return CreateResult{}, err
	}
	hasher := s.issuing
	if cmd.SelfService {
		hasher = s.passwords
	}
	hash, err := hasher.Hash(ctx, oneTime)
	if err != nil {
		return CreateResult{}, fmt.Errorf("hash password: %w", err)
	}

	// One transaction: account, roles, message and audit entries together.
	var created User
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		created, err = s.repo.Create(ctx, User{
			Login:              login,
			Email:              email,
			FullName:           fullName,
			Status:             StatusActive,
			PasswordHash:       hash,
			MustChangePassword: true,
		})
		if err != nil {
			return err
		}

		if len(cmd.Roles) > 0 {
			if err := s.repo.ReplaceRoles(ctx, created.ID, cmd.Roles); err != nil {
				return err
			}
			created.Roles = cmd.Roles
		}

		if err := s.record(ctx, cmd.ActorID, audit.ActionUserCreate, created.ID, map[string]any{
			"login": created.Login,
			"roles": cmd.Roles,
		}); err != nil {
			return err
		}
		if message == nil {
			return nil
		}
		message.UserID = created.ID
		return s.queue(ctx, cmd.ActorID, []mailing.Message{*message})
	})
	if err != nil {
		return CreateResult{}, err
	}
	if message != nil {
		s.wake()
	}

	return CreateResult{User: created, OneTimePassword: oneTime, Email: emailed}, nil
}

// refuseTakenLogin is checked before an account is made, for a clear error;
// the unique index is the real guarantee. A deleted account's login is free.
func (s *Service) refuseTakenLogin(ctx context.Context, login string) error {
	existing, err := s.repo.ByLogin(ctx, login)
	switch {
	case err == nil && existing.Status != StatusDeleted:
		return ErrLoginTaken
	case err != nil && !errors.Is(err, ErrNotFound):
		return err
	}
	return nil
}

// errIncompleteVerifiedAccount means a caller of CreateVerified passed no
// digest or no verification time, or one of CreateExternal an email with no
// verification time: a programming error, never a person's.
var errIncompleteVerifiedAccount = errors.New("users: a verified account needs its password digest and verification time")

// CreateVerifiedCommand is an account its owner asked for and proved the
// email of. PasswordHash is the password they chose, hashed with the sign-in
// hasher.
type CreateVerifiedCommand struct {
	Login, Email, FullName string
	PasswordHash           string
	VerifiedAt             time.Time
}

// CreateVerified makes a student account that signs in with the chosen
// password at once, its email verified. The owner made it, so the audit
// entry has no actor.
func (s *Service) CreateVerified(ctx context.Context, cmd CreateVerifiedCommand) (User, error) {
	login, fullName, email := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName), strings.TrimSpace(cmd.Email)
	if email == "" {
		return User{}, fmt.Errorf("%w: email must not be empty", ErrInvalidAccount)
	}
	if err := ValidateAccount(login, fullName, email); err != nil {
		return User{}, err
	}
	if cmd.PasswordHash == "" || cmd.VerifiedAt.IsZero() {
		return User{}, errIncompleteVerifiedAccount
	}
	roles, err := s.checkRoles(ctx, []string{RoleStudent})
	if err != nil {
		return User{}, err
	}
	if err := s.refuseTakenLogin(ctx, login); err != nil {
		return User{}, err
	}

	var created User
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		created, err = s.repo.Create(ctx, User{
			Login: login, Email: email, FullName: fullName, Status: StatusActive, PasswordHash: cmd.PasswordHash,
		})
		if err != nil {
			return err
		}
		if err := s.repo.MarkEmailVerified(ctx, created.ID, cmd.VerifiedAt); err != nil {
			return err
		}
		created.EmailVerifiedAt = &cmd.VerifiedAt
		if err := s.repo.ReplaceRoles(ctx, created.ID, roles); err != nil {
			return err
		}
		created.Roles = roles
		return s.record(ctx, uuid.Nil, audit.ActionUserCreate, created.ID, map[string]any{
			"login": created.Login,
			"roles": roles,
		})
	})
	if err != nil {
		return User{}, err
	}
	return created, nil
}

// errNoIdentityStore means CreateExternal or LinkExternal ran on a service
// wired without WithExternalIdentities: a wiring fault, never a person's.
var errNoIdentityStore = errors.New("users: linking an external identity needs WithExternalIdentities")

// CreateExternalCommand is an account a person's identity at a provider asked
// for. Email is empty when the provider did not vouch for the address, which
// then stays on Identity only; otherwise it counts as verified at VerifiedAt.
// Identity.UserID is ignored.
type CreateExternalCommand struct {
	Login, FullName, Email string
	VerifiedAt             time.Time
	Identity               ExternalIdentity
}

// CreateExternal makes a student account with no password and links the
// identity to it, in one transaction: a refused link, as when the same person
// signs in twice at once, leaves no account behind. The audit entry, in the
// same transaction, has no actor.
func (s *Service) CreateExternal(ctx context.Context, cmd CreateExternalCommand) (User, error) {
	if s.identities == nil {
		return User{}, errNoIdentityStore
	}
	login, fullName, email := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName), strings.TrimSpace(cmd.Email)
	if err := ValidateAccount(login, fullName, email); err != nil {
		return User{}, err
	}
	if err := cmd.Identity.Validate(); err != nil {
		return User{}, err
	}
	if email != "" && cmd.VerifiedAt.IsZero() {
		return User{}, errIncompleteVerifiedAccount
	}
	roles, err := s.checkRoles(ctx, []string{RoleStudent})
	if err != nil {
		return User{}, err
	}
	if err := s.refuseTakenLogin(ctx, login); err != nil {
		return User{}, err
	}

	var created User
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		created, err = s.repo.Create(ctx, User{Login: login, Email: email, FullName: fullName, Status: StatusActive})
		if err != nil {
			return err
		}
		if email != "" {
			if err := s.repo.MarkEmailVerified(ctx, created.ID, cmd.VerifiedAt); err != nil {
				return err
			}
			created.EmailVerifiedAt = &cmd.VerifiedAt
		}
		if err := s.repo.ReplaceRoles(ctx, created.ID, roles); err != nil {
			return err
		}
		created.Roles = roles
		identity := cmd.Identity
		identity.UserID = created.ID
		if err := s.identities.LinkExternalIdentity(ctx, identity); err != nil {
			return err
		}
		return s.record(ctx, uuid.Nil, audit.ActionUserExternalSignUp, created.ID, map[string]any{
			"login":         created.Login,
			"provider":      identity.Provider,
			"email_trusted": email != "",
		})
	})
	if err != nil {
		return User{}, err
	}
	return created, nil
}

// LinkExternal ties the identity to an existing account and records the
// identity's email as verified at verifiedAt, in one transaction. The account
// is locked and read again first, so a change committed since the caller's
// read is seen: it refuses ErrAccountDeleted, ErrAccountBlocked,
// ErrStaffNotLinkable, ErrNotFound when the account no longer holds the
// identity's email, which is what vouched for linking it, and
// ErrIssuerAlreadyLinked when the account already holds another identity at
// the same issuer: a provider may reassign a mailbox, and the address must
// not carry its new holder into the previous one's account. An account still
// on its one-time password loses it, as retireOneTimePassword says. It
// returns the account as it then stands. As for CreateExternal, the owner
// made the change, so the audit entries have no actor.
func (s *Service) LinkExternal(ctx context.Context, userID uuid.UUID, identity ExternalIdentity, verifiedAt time.Time) (User, error) {
	if s.identities == nil {
		return User{}, errNoIdentityStore
	}
	identity.UserID = userID
	if err := identity.Validate(); err != nil {
		return User{}, err
	}
	var u User
	retired := false
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, userID); err != nil {
			return err
		}
		var err error
		u, err = s.repo.ByID(ctx, userID)
		if err != nil {
			return err
		}
		if err := refuseExternalEntry(u); err != nil {
			return err
		}
		if identity.Email == "" || !strings.EqualFold(u.Email, identity.Email) {
			return fmt.Errorf("%w: the account no longer holds the address", ErrNotFound)
		}
		other, err := s.identities.HoldsOtherIdentityAt(ctx, userID, identity.Provider, identity.Issuer, identity.Subject)
		if err != nil {
			return err
		}
		if other {
			return ErrIssuerAlreadyLinked
		}
		if err := s.identities.LinkExternalIdentity(ctx, identity); err != nil {
			return err
		}
		if err := s.repo.MarkEmailVerified(ctx, userID, verifiedAt); err != nil {
			return err
		}
		u.EmailVerifiedAt = &verifiedAt
		if err := s.record(ctx, uuid.Nil, audit.ActionUserExternalIdentityLink, userID, map[string]any{
			"provider":      identity.Provider,
			"email_trusted": true,
		}); err != nil {
			return err
		}
		if !u.MustChangePassword {
			return nil
		}
		retired = true
		return s.retireOneTimePassword(ctx, &u, identity.Provider)
	})
	if err != nil {
		return User{}, err
	}
	if retired {
		s.forget(ctx, userID)
	}
	return u, nil
}

// retireOneTimePassword runs inside the caller's transaction. The provider
// vouched for the person, who never had the issued password; kept, it would
// send them to a change form that asks for it, and cleared alone, the flag
// would leave a handed-out secret valid. Sessions opened with it end. The
// owner made the change, so the entry has no actor.
func (s *Service) retireOneTimePassword(ctx context.Context, u *User, provider string) error {
	if err := s.repo.SetPassword(ctx, u.ID, "", false); err != nil {
		return err
	}
	generation, err := s.repo.BumpSessionGeneration(ctx, u.ID)
	if err != nil {
		return err
	}
	u.PasswordHash, u.MustChangePassword, u.SessionGeneration = "", false, generation
	return s.record(ctx, uuid.Nil, audit.ActionUserOneTimePasswordRetire, u.ID, map[string]any{"provider": provider})
}

// refuseExternalEntry says why an account read under its lock cannot be
// entered or linked through an external provider, if it cannot.
func refuseExternalEntry(u User) error {
	switch {
	case u.Status == StatusDeleted:
		return ErrAccountDeleted
	case !u.IsActive():
		return ErrAccountBlocked
	case u.IsStaff():
		return ErrStaffNotLinkable
	}
	return nil
}

func (s *Service) List(ctx context.Context, f Filter) ([]User, int, error) {
	if err := f.Validate(); err != nil {
		return nil, 0, err
	}
	return s.repo.List(ctx, f.Normalize())
}

func (s *Service) ByID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.ByID(ctx, id)
}

// setStatus is the single-account form of BulkSetStatus, so the guards
// cannot drift. A skip that a bulk caller would see is returned here as its
// sentinel.
func (s *Service) setStatus(ctx context.Context, actorID, userID uuid.UUID, status, reason string) error {
	res, err := s.BulkSetStatus(ctx, actorID, []uuid.UUID{userID}, status, reason)
	if err != nil {
		return err
	}
	if len(res.Skipped) == 0 {
		return nil
	}
	switch res.Skipped[0].Reason {
	case SkipNotFound:
		return ErrNotFound
	case SkipSelf:
		return ErrCannotActOnSelf
	case SkipLastAdministrator:
		return ErrLastAdministrator
	case SkipLoginTaken:
		return ErrLoginTaken
	case SkipEmailTaken:
		return ErrEmailTaken
	case SkipAlreadyInStatus:
		// Already in that status: a success.
		return nil
	default:
		// A skip reason this switch does not know: the two have drifted.
		return fmt.Errorf("%w: %q", errUnhandledSkipReason, res.Skipped[0].Reason)
	}
}

// Block denies an account access and ends its sessions. The reason is
// mandatory.
func (s *Service) Block(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusBlocked, reason)
}

// Unblock restores access; old sessions stay retired.
func (s *Service) Unblock(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
}

// Delete removes an account without removing what it did (see
// StatusDeleted). The reason is mandatory.
func (s *Service) Delete(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusDeleted, reason)
}

// Restore brings a deleted account back to active, refusing with
// ErrLoginTaken or ErrEmailTaken when a live account has taken either since.
func (s *Service) Restore(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
}

type ChangePasswordCommand struct {
	UserID      uuid.UUID
	OldPassword string
	NewPassword string
}

// ChangePassword replaces a password after checking the current one. The
// caller throttles it (CLAUDE.md rule 4).
func (s *Service) ChangePassword(ctx context.Context, cmd ChangePasswordCommand) error {
	user, err := s.repo.ByID(ctx, cmd.UserID)
	if err != nil {
		return err
	}

	// The current password stops a borrowed browser becoming a takeover.
	matched, err := s.passwords.Verify(ctx, user.PasswordHash, cmd.OldPassword)
	if errors.Is(err, password.ErrBusy) {
		// Busy is not a verdict on the password.
		return err
	}
	if err != nil || !matched {
		return ErrWrongPassword
	}
	if err := ValidatePassword(cmd.NewPassword); err != nil {
		return err
	}
	if cmd.OldPassword == cmd.NewPassword {
		return ErrSamePassword
	}

	hash, err := s.passwords.Hash(ctx, cmd.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPassword(ctx, user.ID, hash, false); err != nil {
			return err
		}
		// Other sessions end: a suspected intruder is why people change it.
		if _, err := s.repo.BumpSessionGeneration(ctx, user.ID); err != nil {
			return err
		}
		return s.record(ctx, user.ID, audit.ActionPasswordChange, user.ID, nil)
	})
	if err != nil {
		return err
	}
	s.forget(ctx, user.ID)
	return nil
}

// PasswordByLinkCommand is a password the account's owner chose after
// following a link mailed to its address, hashed with the sign-in hasher.
type PasswordByLinkCommand struct {
	PasswordHash string
	// At is when the link was followed, and so when the address was proved.
	At time.Time
	// Claim runs first, inside the transaction: it spends the link and names
	// the account it was for, or reports false, and then what it wrote is
	// kept and nothing else changes.
	Claim func(ctx context.Context) (uuid.UUID, bool, error)
}

// SetPasswordByLink sets the password of the account cmd.Claim names, which
// signs in with it at once, marks the address verified and ends every
// session. It reports false when the claim was refused.
func (s *Service) SetPasswordByLink(ctx context.Context, cmd PasswordByLinkCommand) (bool, error) {
	var id uuid.UUID
	done := false
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var ok bool
		var err error
		if id, ok, err = cmd.Claim(ctx); err != nil || !ok {
			return err
		}
		if err := s.repo.SetPassword(ctx, id, cmd.PasswordHash, false); err != nil {
			return err
		}
		if err := s.repo.MarkEmailVerified(ctx, id, cmd.At); err != nil {
			return err
		}
		if _, err := s.repo.BumpSessionGeneration(ctx, id); err != nil {
			return err
		}
		done = true
		return s.record(ctx, id, audit.ActionUserPasswordResetByLink, id, nil)
	})
	if err != nil {
		return false, err
	}
	if done {
		s.forget(ctx, id)
	}
	return done, nil
}

// ResetPassword issues a fresh one-time password for the administrator to
// hand over, and emails it too when delivery asks.
func (s *Service) ResetPassword(ctx context.Context, actorID, userID uuid.UUID, delivery Delivery) (IssuedPassword, error) {
	if err := s.refuseMailOff(delivery); err != nil {
		return IssuedPassword{}, err
	}
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return IssuedPassword{}, err
	}
	if user.Status == StatusDeleted {
		return IssuedPassword{}, ErrAccountDeleted
	}
	letters, err := s.letterheadFor(ctx, delivery)
	if err != nil {
		return IssuedPassword{}, err
	}

	oneTime, err := generatePassword()
	if err != nil {
		return IssuedPassword{}, err
	}
	message, emailed, err := letters.signIn(user, oneTime)
	if err != nil {
		return IssuedPassword{}, err
	}
	hash, err := s.issuing.Hash(ctx, oneTime)
	if err != nil {
		return IssuedPassword{}, fmt.Errorf("hash password: %w", err)
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPassword(ctx, userID, hash, true); err != nil {
			return err
		}
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		if err := s.record(ctx, actorID, audit.ActionUserPasswordReset, userID, nil); err != nil {
			return err
		}
		if err := s.withdraw(ctx, []uuid.UUID{userID}, withdrawnSuperseded); err != nil {
			return err
		}
		if message == nil {
			return nil
		}
		kept, dropped, err := s.keepActive(ctx, []mailing.Message{*message})
		if err != nil {
			return err
		}
		if dropped[userID] {
			message, emailed = nil, EmailOutcome{SkipReason: EmailSkipNotActive}
		}
		return s.queue(ctx, actorID, kept)
	})
	if err != nil {
		return IssuedPassword{}, err
	}
	s.forget(ctx, userID)
	if message != nil {
		s.wake()
	}
	return IssuedPassword{ID: user.ID, Login: user.Login, OneTimePassword: oneTime, Email: emailed}, nil
}

// UpdateProfile changes an account's name and email. A new address loses the
// verified mark until its owner proves it again.
func (s *Service) UpdateProfile(ctx context.Context, actorID, userID uuid.UUID, fullName, email string) error {
	current, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}
	// A deleted account's email has no live index entry to check against.
	if current.Status == StatusDeleted {
		return ErrAccountDeleted
	}

	fullName, email = strings.TrimSpace(fullName), strings.TrimSpace(email)
	if err := ValidateAccount(current.Login, fullName, email); err != nil {
		return err
	}

	changes := audit.Between(current.auditFields(), User{FullName: fullName, Email: email}.auditFields())
	// Verification proves an address, not an account: another address has
	// not been proved, while a change of letter case names the same mailbox.
	keepVerified := strings.EqualFold(current.Email, email)
	if current.EmailVerifiedAt != nil && !keepVerified {
		changes.Set("email_verified", true, false)
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateProfile(ctx, userID, fullName, email, keepVerified); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserUpdate, userID, changes.Payload())
	})
}

func (s *Service) ReplaceRoles(ctx context.Context, actorID, userID uuid.UUID, roleCodes []string) error {
	roleCodes, err := s.checkRoles(ctx, roleCodes)
	if err != nil {
		return err
	}
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Status == StatusDeleted {
		return ErrAccountDeleted
	}

	if holdsAdmin(user.Roles) && !slices.Contains(roleCodes, RoleAdmin) {
		if err := s.refuseIfLastAdmin(ctx, user); err != nil {
			return err
		}
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceRoles(ctx, userID, roleCodes); err != nil {
			return err
		}
		// A demotion must bite now, not at the next login.
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		changes := audit.NewChanges()
		changes.Set("roles", user.Roles, roleCodes)
		return s.record(ctx, actorID, audit.ActionUserRolesChange, userID, changes.Payload())
	})
	if err != nil {
		return err
	}
	s.forget(ctx, userID)
	return nil
}

// entry builds an audit entry for an action on an account, for both single
// and bulk operations.
func (s *Service) entry(actorID uuid.UUID, action string, subject uuid.UUID, payload map[string]any) audit.Entry {
	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	return audit.Entry{
		ActorID:  actor,
		Action:   action,
		Entity:   "user",
		EntityID: subject.String(),
		Payload:  payload,
	}
}

// record appends an audit entry for an action on an account. A failure is
// returned, and the caller's unit of work rolls the action back with it.
func (s *Service) record(ctx context.Context, actorID uuid.UUID, action string, subject uuid.UUID, payload map[string]any) error {
	return s.audit.Record(ctx, s.entry(actorID, action, subject, payload))
}

// ValidateAccount checks trimmed descriptive fields. A login or name with a
// control character is refused: PostgreSQL cannot store a NUL, and the
// sign-in message must not carry one. The email must parse back to exactly
// the bare address, since net/mail also accepts a display name with brackets.
func ValidateAccount(login, fullName, email string) error {
	switch {
	case login == "":
		return fmt.Errorf("%w: login must not be empty", ErrInvalidAccount)
	case len(login) > MaxLoginLength:
		return fmt.Errorf("%w: login must be at most %d characters", ErrInvalidAccount, MaxLoginLength)
	case fullName == "":
		return fmt.Errorf("%w: full name must not be empty", ErrInvalidAccount)
	case len(fullName) > MaxFullNameLength:
		return fmt.Errorf("%w: full name must be at most %d characters", ErrInvalidAccount, MaxFullNameLength)
	case strings.ContainsFunc(login, unicode.IsControl):
		return fmt.Errorf("%w: login must not contain control characters", ErrInvalidAccount)
	case strings.ContainsFunc(fullName, unicode.IsControl):
		return fmt.Errorf("%w: full name must not contain control characters", ErrInvalidAccount)
	}

	if email == "" {
		return nil
	}
	if len(email) > MaxEmailLength {
		return fmt.Errorf("%w: email must be at most %d characters", ErrInvalidAccount, MaxEmailLength)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalidAccount, email)
	}
	return nil
}

// checkRoles returns the codes without repeats, refusing more than
// MaxRolesPerAccount of them or one the role catalog does not hold.
func (s *Service) checkRoles(ctx context.Context, codes []string) ([]string, error) {
	if len(codes) == 0 {
		return codes, nil
	}
	distinct := make([]string, 0, min(len(codes), MaxRolesPerAccount))
	for _, code := range codes {
		if slices.Contains(distinct, code) {
			continue
		}
		if len(distinct) == MaxRolesPerAccount {
			return nil, fmt.Errorf("%w: at most %d roles", ErrInvalidAccount, MaxRolesPerAccount)
		}
		distinct = append(distinct, code)
	}

	catalogue, err := s.repo.Roles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	for _, code := range distinct {
		if !slices.ContainsFunc(catalogue, func(r Role) bool { return r.Code == code }) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownRole, code)
		}
	}
	return distinct, nil
}

func validateReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return "", ErrReasonRequired
	case len(reason) > MaxStatusReasonLength:
		return "", fmt.Errorf("%w: the reason must be at most %d characters",
			ErrInvalidAccount, MaxStatusReasonLength)
	}
	return reason, nil
}

// ValidatePassword applies the policy. The maximum is the hasher's, in
// bytes: argon2 hashes the whole input, and Hasher.Hash refuses a longer one
// with an undeclared error, which would be answered as a server fault.
func ValidatePassword(candidate string) error {
	if len([]rune(candidate)) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if len(candidate) > password.MaxLength {
		return fmt.Errorf("%w: at most %d bytes", ErrWeakPassword, password.MaxLength)
	}
	if strings.TrimSpace(candidate) == "" {
		return fmt.Errorf("%w: must not be only whitespace", ErrWeakPassword)
	}
	return nil
}

func generatePassword() (string, error) {
	raw := make([]byte, oneTimePasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func holdsAdmin(roles []string) bool { return slices.Contains(roles, RoleAdmin) }

// refuseIfLastAdmin blocks a change that would leave nobody able to
// administer. The count and the write are not one transaction, so two
// simultaneous demotions could both pass; that rare case is accepted rather
// than serialising every role change.
func (s *Service) refuseIfLastAdmin(ctx context.Context, user User) error {
	// Blocked administrators are not counted, nor is this one.
	remaining, err := s.repo.CountActiveWithRole(ctx, RoleAdmin)
	if err != nil {
		return fmt.Errorf("count administrators: %w", err)
	}
	if user.IsActive() {
		remaining--
	}
	if remaining <= 0 {
		return ErrLastAdministrator
	}
	return nil
}

const RoleAdmin = "admin"

// RoleStudent holds no permission: a participant.
const RoleStudent = "student"

type BootstrapResult struct {
	User User
	// OneTimePassword is set only when an account was created.
	OneTimePassword string
	Created         bool
}

// BootstrapAdmin makes sure an administrator account exists. It is
// idempotent and run from the command line (cmd/bootstrap), never over HTTP.
func (s *Service) BootstrapAdmin(ctx context.Context, login, fullName string) (BootstrapResult, error) {
	existing, err := s.repo.ByLogin(ctx, login)
	switch {
	// A deleted match's login is free, so a new account is created.
	case err == nil && existing.Status != StatusDeleted:
		return BootstrapResult{User: existing}, nil
	case err != nil && !errors.Is(err, ErrNotFound):
		return BootstrapResult{}, err
	}

	// No actor: nobody was signed in.
	created, err := s.Create(ctx, CreateCommand{
		Login:    login,
		FullName: fullName,
		Roles:    []string{RoleAdmin},
	})
	if err != nil {
		return BootstrapResult{}, err
	}

	return BootstrapResult{
		User:            created.User,
		OneTimePassword: created.OneTimePassword,
		Created:         true,
	}, nil
}

const (
	SkipLoginTaken = "login_taken"
	SkipEmailTaken = "email_taken"
	SkipInvalidRow = "invalid_row"
)

// maxImportRows bounds a roster (CLAUDE.md rule 2): every row costs an
// argon2id hash.
const maxImportRows = 500

var ErrRosterTooLarge = errors.New("too many rows in one import")

type ImportRow struct {
	Login    string
	FullName string
	Email    string
}

// ImportCommand creates accounts for a whole group.
type ImportCommand struct {
	ActorID uuid.UUID
	Rows    []ImportRow
	// Roles every created account receives.
	Roles    []string
	Delivery Delivery
}

// ImportResult reports what an import did, row by row; one bad row does not
// reject the others.
type ImportResult struct {
	Created []CreateResult
	Skipped []SkippedRow
	// NotImported names the rows an import that stopped with an error never
	// reached, from the failing row on. It travels with the error, so the
	// accounts already created and their passwords are not lost.
	NotImported []string
}

type SkippedRow struct {
	// Login is exactly as given, so the line can be found.
	Login  string
	Reason string
}

// Import creates an account for every row it can use, each in its own unit
// of work, so one bad row rolls back only itself. A row's message is queued
// in its row's unit of work.
func (s *Service) Import(ctx context.Context, cmd ImportCommand) (ImportResult, error) {
	if err := s.refuseMailOff(cmd.Delivery); err != nil {
		return ImportResult{}, err
	}
	if len(cmd.Rows) > maxImportRows {
		return ImportResult{}, fmt.Errorf("%w: %d rows, at most %d",
			ErrRosterTooLarge, len(cmd.Rows), maxImportRows)
	}

	// Checked once for the whole roster: a bad role is the request's fault,
	// not any row's.
	roles, err := s.checkRoles(ctx, cmd.Roles)
	if err != nil {
		return ImportResult{}, err
	}
	letters, err := s.letterheadFor(ctx, cmd.Delivery)
	if err != nil {
		return ImportResult{}, err
	}

	var result ImportResult
	for i, row := range cmd.Rows {
		created, err := s.create(ctx, CreateCommand{
			ActorID:  cmd.ActorID,
			Login:    row.Login,
			Email:    row.Email,
			FullName: row.FullName,
			Roles:    roles,
		}, letters)
		switch {
		case err == nil:
			result.Created = append(result.Created, created)
		case errors.Is(err, ErrLoginTaken):
			result.Skipped = append(result.Skipped, SkippedRow{Login: row.Login, Reason: SkipLoginTaken})
		case errors.Is(err, ErrEmailTaken):
			result.Skipped = append(result.Skipped, SkippedRow{Login: row.Login, Reason: SkipEmailTaken})
		case errors.Is(err, ErrInvalidAccount):
			result.Skipped = append(result.Skipped, SkippedRow{Login: row.Login, Reason: SkipInvalidRow})
		default:
			// Anything else is an outage, not a bad row: abort and report
			// what was and was not done (CLAUDE.md rule 8).
			for _, rest := range cmd.Rows[i:] {
				result.NotImported = append(result.NotImported, rest.Login)
			}
			return result, fmt.Errorf("import row %q: %w", row.Login, err)
		}
	}
	return result, nil
}
