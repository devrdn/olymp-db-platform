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

// errUnhandledSkipReason means setStatus's translation switch does not know a
// skip reason BulkSetStatus returned. It is unexported: reaching it is
// exclusively a programming error inside this package (the two have drifted
// apart), never a refusal a caller can act on, so there is nothing for
// another package to match against.
var errUnhandledSkipReason = errors.New("users: unhandled skip reason for a single-account operation")

// Service holds the account rules.
type Service struct {
	repo  Repository
	audit *audit.Recorder
	uow   storage.UnitOfWork
	// passwords checks and sets a password for the account's owner.
	passwords *password.Hasher
	// issuing sets the passwords an administrator hands out.
	issuing *password.Hasher
	// access is told which accounts changed in a way the authentication path
	// must notice. Nil when nothing caches accounts.
	access AccessCache
}

// AccessCache is a short-lived copy of accounts kept by the authentication
// path, so it does not have to read the account on every request.
//
// It is declared here, by the package that knows when an account changes,
// and implemented by package auth, which keeps the copy. Every operation that
// changes what authentication decides on — the status, the roles and with
// them the permissions, the password and its one-time flag, the session
// generation — names the accounts it changed once the change has committed.
//
// Forget reports nothing back. The change has already landed by then and
// cannot be undone for a cache's sake; the implementation logs a failure, and
// a copy it could not drop still expires on its own after a few seconds.
type AccessCache interface {
	Forget(ctx context.Context, ids []uuid.UUID)
}

// WithAccessCache sets the cache to tell about changes to access. It is meant
// for the composition root, before the service is shared.
func (s *Service) WithAccessCache(c AccessCache) *Service {
	s.access = c
	return s
}

// forget tells the access cache that the accounts changed. It is called once
// the unit of work has returned, which is when the change commits: told
// earlier, a request landing in between would read the old row and cache it
// again. That holds because no caller runs these operations inside a
// transaction of its own; one that did would commit later than this, and a
// copy cached in that gap would outlive the change by the cache's lifetime.
//
// The caller's cancellation is not passed on. The change has landed whether
// or not the administrator's browser is still waiting, and a request that
// hangs up at the wrong moment must not leave the old state cached.
func (s *Service) forget(ctx context.Context, ids ...uuid.UUID) {
	if s.access == nil || len(ids) == 0 {
		return
	}
	s.access.Forget(context.WithoutCancel(ctx), ids)
}

// NewService assembles the account service. Every multi-write operation runs
// inside uow, so an action and its audit entry land together or not at all.
//
// hasher is the process's one password hasher, shared with sign-in: its bound
// on concurrent hashing only holds if every caller goes through the same one.
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

// administrativeHashWait is how long issuing a password — creating an
// account, resetting one, a roster import or a bulk reset — waits for a
// hashing slot, against the sign-in wait of a couple of seconds.
//
// These callers are authenticated administrators, their batches are bounded
// (maxImportRows, MaxBulkAccounts), and an import that gives up halfway
// through has already shown one-time passwords the response then discards.
// So they wait, sharing the same slots, rather than being refused because
// anonymous sign-ins filled them for a moment; the request's own context still
// ends the wait when the administrator's browser gives up.
const administrativeHashWait = 30 * time.Second

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
	login, fullName, email := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName), strings.TrimSpace(cmd.Email)
	if err := validateAccount(login, fullName, email); err != nil {
		return CreateResult{}, err
	}

	// Check before writing so the caller gets a clear error rather than a
	// constraint violation; the unique index remains the real guarantee
	// against a concurrent duplicate.
	//
	// ByLogin still returns a deleted account when nothing live has reclaimed
	// its login (see its comment in internal/postgres/users.go) — that is
	// deliberate for sign-in, but here it must not read as "taken": a deleted
	// account is exactly the case this recreates, and refusing it would be
	// the false ErrLoginTaken the deletion feature exists to avoid. The
	// partial unique index does not cover deleted rows either, so the
	// database agrees the login is free.
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

	// One transaction: a user without their roles, or without the entry that
	// says who created them, must not be able to exist.
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

// List returns a page of accounts.
func (s *Service) List(ctx context.Context, f Filter) ([]User, int, error) {
	return s.repo.List(ctx, f.Normalize())
}

// ByID returns one account.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.ByID(ctx, id)
}

// setStatus is the single-account form of BulkSetStatus.
//
// One implementation serves both surfaces, so the guards — refusing
// yourself, the last administrator, or a login another account has taken —
// cannot drift apart between them. What differs is only how a refusal is
// reported: a bulk caller gets a skip with a reason, because the rest of its
// selection still applies; a single-account caller gets the sentinel,
// because for them the skip is the whole outcome and a silent success would
// be a lie.
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
		// The caller wanted the account in that status and it already is.
		// That is a success, not a refusal.
		return nil
	default:
		// Every reason BulkSetStatus can actually produce is named above,
		// including SkipDeleted (BulkSetStatus never emits it today — it
		// belongs to the bulk role and password operations, which must skip a
		// deleted account rather than touch it — but it is still a name in the
		// shared vocabulary). Reaching here means the machine returned a skip
		// reason this translation does not know, which is a programming error
		// — this package and BulkSetStatus have drifted apart — and not
		// anything the caller did, so it is a declared sentinel wrapped with
		// the unknown reason rather than a bare error the HTTP layer cannot
		// name.
		return fmt.Errorf("%w: %q", errUnhandledSkipReason, res.Skipped[0].Reason)
	}
}

// Block denies an account access and ends the sessions it already has.
//
// The reason is mandatory: blocking is a thing an administrator is answered
// to for later, and "no reason given" is not an answer the trail can carry.
func (s *Service) Block(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusBlocked, reason)
}

// Unblock restores access. Existing sessions stay retired: the account has to
// sign in again.
func (s *Service) Unblock(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
}

// Delete removes an account without removing what it did.
//
// The row stays, so results and the audit trail keep their subject, and the
// account cannot sign in, does not count as an administrator and no longer
// holds its login. The reason is mandatory for the same reason blocking's is:
// deletion is answered to later.
func (s *Service) Delete(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusDeleted, reason)
}

// Restore brings a deleted account back to active. It refuses with
// ErrLoginTaken or ErrEmailTaken when a live account has taken the login or
// email in the meantime — the direct price of releasing them on deletion.
func (s *Service) Restore(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
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
	matched, err := s.passwords.Verify(ctx, user.PasswordHash, cmd.OldPassword)
	if errors.Is(err, password.ErrBusy) {
		// Load, not a verdict: telling somebody who typed their password
		// correctly that they did not would be the wrong answer.
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
		// Changing a password is what someone does when they suspect the
		// account is in use elsewhere; those sessions have to end.
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

// ResetPassword issues a fresh one-time password for an account the user can
// no longer reach, and returns it for the administrator to hand over.
func (s *Service) ResetPassword(ctx context.Context, actorID, userID uuid.UUID) (string, error) {
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return "", err
	}
	// A deleted account cannot sign in, so a new password for it is
	// pointless — the same reasoning BulkResetPassword already applies to a
	// selection (see SkipDeleted); this is what the single-account path
	// answers with instead of quietly issuing a password nobody can use.
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

// UpdateProfile changes the descriptive fields of an account.
func (s *Service) UpdateProfile(ctx context.Context, actorID, userID uuid.UUID, fullName, email string) error {
	current, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}
	// A deleted account has no screen to read the new name or email from, and
	// no live index entry to check the new email against — editing it is not
	// a change anybody can observe. Neither bulk nor single-account path had
	// a guard here before; this gives the single-account one the same refusal
	// the other two single-account writes below now carry.
	if current.Status == StatusDeleted {
		return ErrAccountDeleted
	}

	fullName, email = strings.TrimSpace(fullName), strings.TrimSpace(email)
	if err := validateAccount(current.Login, fullName, email); err != nil {
		return err
	}

	// What moved and what it was. The new name alone said neither what it
	// replaced nor whether the email had changed at all.
	changes := audit.Between(current.auditFields(), User{FullName: fullName, Email: email}.auditFields())

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.UpdateProfile(ctx, userID, fullName, email); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionUserUpdate, userID, changes.Payload())
	})
}

// ReplaceRoles sets an account's global roles.
func (s *Service) ReplaceRoles(ctx context.Context, actorID, userID uuid.UUID, roleCodes []string) error {
	user, err := s.repo.ByID(ctx, userID)
	if err != nil {
		return err
	}
	// A deleted account cannot sign in, so giving it roles is pointless —
	// mirroring BulkReplaceRoles's own SkipDeleted guard, which skips a
	// deleted account in a selection for the same reason.
	if user.Status == StatusDeleted {
		return ErrAccountDeleted
	}

	// Taking the administrator role away from the only one left leaves nobody
	// able to put it back.
	if holdsAdmin(user.Roles) && !slices.Contains(roleCodes, RoleAdmin) {
		if err := s.refuseIfLastAdmin(ctx, user); err != nil {
			return err
		}
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceRoles(ctx, userID, roleCodes); err != nil {
			return err
		}
		// New limits have to bite immediately: a demotion that waited for the
		// next login would leave someone exercising rights they no longer hold.
		if _, err := s.repo.BumpSessionGeneration(ctx, userID); err != nil {
			return err
		}
		// One shape for every change, so the panel can render it without
		// knowing which action it is looking at.
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

// entry builds an audit entry for an action on an account.
//
// record and the bulk operations both build entries through this, so the
// single and batch trails cannot drift apart.
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

// record appends an audit entry for an action on an account.
//
// A failure is returned rather than swallowed: for privileged operations, an
// action nobody can account for afterwards is worse than a failed one. Callers
// run these inside a unit of work, so the action rolls back with the entry.
func (s *Service) record(ctx context.Context, actorID uuid.UUID, action string, subject uuid.UUID, payload map[string]any) error {
	return s.audit.Record(ctx, s.entry(actorID, action, subject, payload))
}

// validateAccount checks the descriptive fields an account is created or
// updated with. The values are expected already trimmed.
//
// The email is parsed rather than pattern-matched, and has to come back as
// exactly the bare address that went in: net/mail also accepts a display name
// with brackets around the address, which is a valid header and not a valid
// thing to store in a column other code will send mail to.
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

// validateReason checks the explanation a status change carries.
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

func holdsAdmin(roles []string) bool { return slices.Contains(roles, RoleAdmin) }

// refuseIfLastAdmin blocks a change that would leave nobody able to administer.
//
// Counted rather than remembered: the number changes under this process, and a
// cached answer would be wrong exactly when two administrators are being
// removed at once. The count and the write are not in one transaction, so two
// simultaneous demotions could still both pass — the window is milliseconds
// and the failure is recoverable by the other administrator, which is a fair
// trade against serialising every role change in the installation.
func (s *Service) refuseIfLastAdmin(ctx context.Context, user User) error {
	// A blocked administrator cannot administer, so they do not count. Nor
	// does this one, whose administrator role is what is being taken away.
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
	// A deleted match is not the reachable administrator this is idempotent
	// about — its login is free, exactly as Create treats it — so this falls
	// through to creating a fresh one below rather than reporting the
	// deleted account as "already exists".
	case err == nil && existing.Status != StatusDeleted:
		return BootstrapResult{User: existing}, nil
	case err != nil && !errors.Is(err, ErrNotFound):
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

// Why a row of an import produced no account.
const (
	SkipLoginTaken = "login_taken"
	SkipEmailTaken = "email_taken"
	SkipInvalidRow = "invalid_row"
)

// maxImportRows bounds a roster.
//
// Every row costs an argon2id hash, which is deliberately expensive: 64 MiB
// and three passes. An unbounded list would be a way to spend the server's
// memory and CPU with one request, on an endpoint an organizer legitimately
// holds. A university group is thirty people and a whole year is a few
// hundred, so this is far above any honest use.
const maxImportRows = 500

// ErrRosterTooLarge reports an import above that bound.
var ErrRosterTooLarge = errors.New("too many rows in one import")

// ImportRow is one line of a roster.
type ImportRow struct {
	Login    string
	FullName string
	Email    string
}

// ImportCommand creates accounts for a whole group.
//
// Accounts are created by an administrator rather than by self-registration
// (see §7), and a group arrives as a list from the department — so creating
// them one request at a time is thirty round trips and thirty chances to lose
// one.
type ImportCommand struct {
	ActorID uuid.UUID
	Rows    []ImportRow
	// Roles every created account receives. Typically the single student role.
	Roles []string
}

// ImportResult reports what an import did, row by row.
//
// Partial success is the honest outcome, exactly as it is for a contest
// roster: one duplicate must not reject the other twenty-nine, and whoever
// pasted the list has to see which line to fix.
type ImportResult struct {
	Created []CreateResult
	Skipped []SkippedRow
}

// SkippedRow is one line that produced no account.
type SkippedRow struct {
	// Login is the value exactly as it was given, so the line can be found
	// again in the list it came from.
	Login  string
	Reason string
}

// Import creates an account for every row it can use.
//
// Each account is created in its own unit of work, through the same path as a
// single creation: one bad row rolls back its own row and nothing else. The
// alternative — one transaction for the whole roster — would make the
// twenty-ninth duplicate discard the twenty-eight accounts before it, and the
// one-time passwords already shown for them.
func (s *Service) Import(ctx context.Context, cmd ImportCommand) (ImportResult, error) {
	if len(cmd.Rows) > maxImportRows {
		return ImportResult{}, fmt.Errorf("%w: %d rows, at most %d",
			ErrRosterTooLarge, len(cmd.Rows), maxImportRows)
	}

	var result ImportResult
	for _, row := range cmd.Rows {
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
			// A row that could never be an account — no login, no name, an
			// address that is not one. The message is not carried: the line
			// number is what the importer needs.
			result.Skipped = append(result.Skipped, SkippedRow{Login: row.Login, Reason: SkipInvalidRow})
		default:
			// Anything else is the database or the trail failing, not the
			// row. Reporting it as "invalid row" would tell the importer to
			// fix a line that was fine, and hide an outage behind a list of
			// them; the accounts already created stay created and are
			// reported by the error, not swallowed.
			return result, fmt.Errorf("import row %q: %w", row.Login, err)
		}
	}
	return result, nil
}
