package users

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// Password policy. Length is the requirement that actually correlates with
// strength; composition rules mostly push people towards "Password1!".
const (
	MinPasswordLength = 12

	// oneTimePasswordBytes is the entropy of a generated password. It is read
	// out loud or pasted once and then replaced, so it favours entropy over
	// being memorable.
	oneTimePasswordBytes = 12
)

// ErrCannotActOnSelf guards the operations that would lock an administrator
// out of the installation.
var ErrCannotActOnSelf = errors.New("this operation cannot be performed on your own account")

// Service holds the account rules.
type Service struct {
	repo  Repository
	audit *audit.Recorder
	uow   storage.UnitOfWork
}

// NewService assembles the account service. Every multi-write operation runs
// inside uow, so an action and its audit entry land together or not at all.
func NewService(repo Repository, recorder *audit.Recorder, uow storage.UnitOfWork) *Service {
	return &Service{repo: repo, audit: recorder, uow: uow}
}

// CreateCommand describes a new account.
type CreateCommand struct {
	ActorID  uuid.UUID
	Login    string
	Email    string
	FullName string
	Roles    []string
}

// CreateResult carries the account and the password to hand over.
type CreateResult struct {
	User User
	// OneTimePassword is returned exactly once, at creation. It is never
	// stored in clear and cannot be retrieved later — a lost one is reset.
	OneTimePassword string
}

// Create registers an account with a generated one-time password.
//
// The administrator never chooses a password that outlives the handover: the
// account is flagged so the first login ends in the user setting their own.
func (s *Service) Create(ctx context.Context, cmd CreateCommand) (CreateResult, error) {
	login := strings.TrimSpace(cmd.Login)
	if login == "" {
		return CreateResult{}, errors.New("login must not be empty")
	}
	if strings.TrimSpace(cmd.FullName) == "" {
		return CreateResult{}, errors.New("full name must not be empty")
	}

	// Check before writing so the caller gets a clear error rather than a
	// constraint violation; the unique index remains the real guarantee
	// against a concurrent duplicate.
	if _, err := s.repo.ByLogin(ctx, login); err == nil {
		return CreateResult{}, ErrLoginTaken
	} else if !errors.Is(err, ErrNotFound) {
		return CreateResult{}, err
	}

	oneTime, err := generatePassword()
	if err != nil {
		return CreateResult{}, err
	}
	hash, err := password.Hash(oneTime)
	if err != nil {
		return CreateResult{}, fmt.Errorf("hash password: %w", err)
	}

	// One transaction: a user without their roles, or without the entry that
	// says who created them, must not be able to exist.
	var created User
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		created, err = s.repo.Create(ctx, User{
			Login:              login,
			Email:              strings.TrimSpace(cmd.Email),
			FullName:           strings.TrimSpace(cmd.FullName),
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

// List returns a page of accounts.
func (s *Service) List(ctx context.Context, f Filter) ([]User, int, error) {
	return s.repo.List(ctx, f.Normalize())
}

// ByID returns one account.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.ByID(ctx, id)
}

// Block denies an account access and ends the sessions it already has.
func (s *Service) Block(ctx context.Context, actorID, userID uuid.UUID) error {
	if actorID == userID {
		return ErrCannotActOnSelf
	}
	if _, err := s.repo.ByID(ctx, userID); err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetStatus(ctx, userID, StatusBlocked); err != nil {
			return err
		}
		// Without this the account keeps working in every tab that is already
		// open, which is precisely the situation blocking exists to stop.
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserBlock, userID, nil)
	})
}

// Unblock restores access. Existing sessions stay retired: the account has to
// sign in again.
func (s *Service) Unblock(ctx context.Context, actorID, userID uuid.UUID) error {
	if _, err := s.repo.ByID(ctx, userID); err != nil {
		return err
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetStatus(ctx, userID, StatusActive); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserUnblock, userID, nil)
	})
}

// ChangePasswordCommand is a user changing their own password.
type ChangePasswordCommand struct {
	UserID      uuid.UUID
	OldPassword string
	NewPassword string
}

// ChangePassword replaces a password after checking the current one.
func (s *Service) ChangePassword(ctx context.Context, cmd ChangePasswordCommand) error {
	user, err := s.repo.ByID(ctx, cmd.UserID)
	if err != nil {
		return err
	}

	// Proving knowledge of the current password is what stops a borrowed
	// unlocked browser from becoming a permanent takeover.
	matched, err := password.Verify(user.PasswordHash, cmd.OldPassword)
	if err != nil || !matched {
		return ErrWrongPassword
	}
	if err := validatePassword(cmd.NewPassword); err != nil {
		return err
	}
	if cmd.OldPassword == cmd.NewPassword {
		return ErrSamePassword
	}

	hash, err := password.Hash(cmd.NewPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPassword(ctx, user.ID, hash, false); err != nil {
			return err
		}
		// Changing a password is what someone does when they suspect the
		// account is in use elsewhere; those sessions have to end.
		if _, err := s.repo.BumpSessionGeneration(ctx, user.ID); err != nil {
			return err
		}
		return s.record(ctx, user.ID, audit.ActionPasswordChange, user.ID, nil)
	})
}

// ResetPassword issues a fresh one-time password for an account the user can
// no longer reach, and returns it for the administrator to hand over.
func (s *Service) ResetPassword(ctx context.Context, actorID, userID uuid.UUID) (string, error) {
	if _, err := s.repo.ByID(ctx, userID); err != nil {
		return "", err
	}

	oneTime, err := generatePassword()
	if err != nil {
		return "", err
	}
	hash, err := password.Hash(oneTime)
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
	return oneTime, nil
}

// UpdateProfile changes the descriptive fields of an account.
func (s *Service) UpdateProfile(ctx context.Context, actorID, userID uuid.UUID, fullName, email string) error {
	if _, err := s.repo.ByID(ctx, userID); err != nil {
		return err
	}
	if strings.TrimSpace(fullName) == "" {
		return errors.New("full name must not be empty")
	}
	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateProfile(ctx, userID, strings.TrimSpace(fullName), strings.TrimSpace(email)); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserUpdate, userID, map[string]any{"full_name": fullName})
	})
}

// ReplaceRoles sets an account's global roles.
func (s *Service) ReplaceRoles(ctx context.Context, actorID, userID uuid.UUID, roleCodes []string) error {
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceRoles(ctx, userID, roleCodes); err != nil {
			return err
		}
		// New limits have to bite immediately: a demotion that waited for the
		// next login would leave someone exercising rights they no longer hold.
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserRolesChange, userID, map[string]any{
			"from": user.Roles,
			"to":   roleCodes,
		})
	})
}

// record appends an audit entry for an action on an account.
//
// A failure is returned rather than swallowed: for privileged operations, an
// action nobody can account for afterwards is worse than a failed one. Callers
// run these inside a unit of work, so the action rolls back with the entry.
func (s *Service) record(ctx context.Context, actorID uuid.UUID, action string, subject uuid.UUID, payload map[string]any) error {
	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	return s.audit.Record(ctx, audit.Entry{
		ActorID:  actor,
		Action:   action,
		Entity:   "user",
		EntityID: subject.String(),
		Payload:  payload,
	})
}

// validatePassword applies the policy.
func validatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if strings.TrimSpace(password) == "" {
		return fmt.Errorf("%w: must not be only whitespace", ErrWeakPassword)
	}
	return nil
}

// generatePassword returns a random one-time password.
func generatePassword() (string, error) {
	raw := make([]byte, oneTimePasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// RoleAdmin is the global role that holds every permission.
const RoleAdmin = "admin"

// BootstrapResult reports what the bootstrap did.
type BootstrapResult struct {
	User User
	// OneTimePassword is set only when an account was created. Re-running the
	// bootstrap must not reset a working administrator's password.
	OneTimePassword string
	Created         bool
}

// BootstrapAdmin makes sure an administrator account exists.
//
// A freshly migrated installation has no accounts, so there is no way in that
// does not itself require signing in. This is that way in, and it runs from
// the command line rather than over HTTP: an unauthenticated endpoint that
// creates administrators would be a permanent liability, however carefully it
// were guarded.
//
// It is idempotent by design: a deployment script may run it every time.
func (s *Service) BootstrapAdmin(ctx context.Context, login, fullName string) (BootstrapResult, error) {
	existing, err := s.repo.ByLogin(ctx, login)
	switch {
	case err == nil:
		return BootstrapResult{User: existing}, nil
	case !errors.Is(err, ErrNotFound):
		return BootstrapResult{}, err
	}

	// ActorID is left empty: nobody was signed in, and inventing an actor
	// would make the trail claim something untrue.
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
