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

	"github.com/devrdn/db-contest/backend/internal/audit"
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
}

type CreateResult struct {
	User User
	// OneTimePassword is returned once and never stored in clear.
	OneTimePassword string
}

// Create registers an account with a generated one-time password, which the
// user must replace at first login.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (CreateResult, error) {
	login, fullName, email := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName), strings.TrimSpace(cmd.Email)
	if err := validateAccount(login, fullName, email); err != nil {
		return CreateResult{}, err
	}

	// Checked first for a clear error; the unique index is the real
	// guarantee. A deleted account's login is free, so ByLogin returning one
	// does not count as taken.
	if existing, err := s.repo.ByLogin(ctx, login); err == nil {
		if existing.Status != StatusDeleted {
			return CreateResult{}, ErrLoginTaken
		}
	} else if !errors.Is(err, ErrNotFound) {
		return CreateResult{}, err
	}

	oneTime, err := generatePassword()
	if err != nil {
		return CreateResult{}, err
	}
	hash, err := s.issuing.Hash(ctx, oneTime)
	if err != nil {
		return CreateResult{}, fmt.Errorf("hash password: %w", err)
	}

	// One transaction: account, roles and audit entry together.
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

		return s.record(ctx, cmd.ActorID, audit.ActionUserCreate, created.ID, map[string]any{
			"login": created.Login,
			"roles": cmd.Roles,
		})
	})
	if err != nil {
		return CreateResult{}, err
	}

	return CreateResult{User: created, OneTimePassword: oneTime}, nil
}

func (s *Service) List(ctx context.Context, f Filter) ([]User, int, error) {
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
	if err := validatePassword(cmd.NewPassword); err != nil {
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

// ResetPassword issues a fresh one-time password for the administrator to
// hand over.
func (s *Service) ResetPassword(ctx context.Context, actorID, userID uuid.UUID) (string, error) {
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if user.Status == StatusDeleted {
		return "", ErrAccountDeleted
	}

	oneTime, err := generatePassword()
	if err != nil {
		return "", err
	}
	hash, err := s.issuing.Hash(ctx, oneTime)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPassword(ctx, userID, hash, true); err != nil {
			return err
		}
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserPasswordReset, userID, nil)
	})
	if err != nil {
		return "", err
	}
	s.forget(ctx, userID)
	return oneTime, nil
}

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
	if err := validateAccount(current.Login, fullName, email); err != nil {
		return err
	}

	changes := audit.Between(current.auditFields(), User{FullName: fullName, Email: email}.auditFields())

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateProfile(ctx, userID, fullName, email); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserUpdate, userID, changes.Payload())
	})
}

func (s *Service) ReplaceRoles(ctx context.Context, actorID, userID uuid.UUID, roleCodes []string) error {
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

// validateAccount checks trimmed descriptive fields. The email must parse
// back to exactly the bare address, since net/mail also accepts a display
// name with brackets.
func validateAccount(login, fullName, email string) error {
	switch {
	case login == "":
		return fmt.Errorf("%w: login must not be empty", ErrInvalidAccount)
	case len(login) > MaxLoginLength:
		return fmt.Errorf("%w: login must be at most %d characters", ErrInvalidAccount, MaxLoginLength)
	case fullName == "":
		return fmt.Errorf("%w: full name must not be empty", ErrInvalidAccount)
	case len(fullName) > MaxFullNameLength:
		return fmt.Errorf("%w: full name must be at most %d characters", ErrInvalidAccount, MaxFullNameLength)
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

func validatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if strings.TrimSpace(password) == "" {
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
	Roles []string
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
// of work, so one bad row rolls back only itself.
func (s *Service) Import(ctx context.Context, cmd ImportCommand) (ImportResult, error) {
	if len(cmd.Rows) > maxImportRows {
		return ImportResult{}, fmt.Errorf("%w: %d rows, at most %d",
			ErrRosterTooLarge, len(cmd.Rows), maxImportRows)
	}

	var result ImportResult
	for i, row := range cmd.Rows {
		created, err := s.Create(ctx, CreateCommand{
			ActorID:  cmd.ActorID,
			Login:    row.Login,
			Email:    row.Email,
			FullName: row.FullName,
			Roles:    cmd.Roles,
		})
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
