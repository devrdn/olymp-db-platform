package users

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// MaxBulkAccounts bounds one operation (CLAUDE.md rule 2).
const MaxBulkAccounts = 500

// Why an account in a selection did not change. A closed vocabulary; any
// other failure aborts the operation (CLAUDE.md rule 8).
const (
	SkipNotFound          = "not_found"
	SkipSelf              = "self"
	SkipLastAdministrator = "last_administrator"
	SkipAlreadyInStatus   = "already_in_status"
	// SkipDeleted is for bulk role changes and password resets;
	// BulkSetStatus does act on deleted accounts.
	SkipDeleted = "deleted"
)

var ErrTooManyAccounts = errors.New("too many accounts in one operation")

type SkippedAccount struct {
	ID     uuid.UUID
	Login  string
	Reason string
}

type BulkResult struct {
	Changed []uuid.UUID
	Skipped []SkippedAccount
}

type selection struct {
	accepted []User
	skipped  []SkippedAccount
}

// classify reads the selection once and asks decide about each account, in
// the caller's order. Deciding must see the whole selection: a per-account
// last-admin check would let the last two administrators through, each seeing
// the other. So decisions are made in memory against one administrator
// budget, and the caller applies the survivors in one transaction.
//
// decide returns "" to accept an account, or a skip reason.
func (s *Service) classify(ctx context.Context, ids []uuid.UUID, decide func(User) string) (selection, error) {
	found, err := s.repo.ByIDs(ctx, ids)
	if err != nil {
		return selection{}, err
	}
	byID := make(map[uuid.UUID]User, len(found))
	for _, u := range found {
		byID[u.ID] = u
	}

	var sel selection
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue // a repeated id is one account
		}
		seen[id] = true

		user, ok := byID[id]
		if !ok {
			sel.skipped = append(sel.skipped, SkippedAccount{ID: id, Reason: SkipNotFound})
			continue
		}
		if reason := decide(user); reason != "" {
			sel.skipped = append(sel.skipped, SkippedAccount{ID: id, Login: user.Login, Reason: reason})
			continue
		}
		sel.accepted = append(sel.accepted, user)
	}
	return sel, nil
}

// adminBudget is how many administrators may still be taken away. The count
// is read lazily on the first spend, and only once per selection.
type adminBudget struct {
	ctx       context.Context
	repo      Repository
	fetched   bool
	remaining int
	err       error
}

func (s *Service) newAdminBudget(ctx context.Context) *adminBudget {
	return &adminBudget{ctx: ctx, repo: s.repo}
}

// spend takes one administrator away, or refuses at the last. A count
// failure is kept on the budget, since decide cannot return errors, and
// checked after classify.
func (b *adminBudget) spend() bool {
	if !b.fetched {
		b.fetched = true
		remaining, err := b.repo.CountActiveWithRole(b.ctx, RoleAdmin)
		if err != nil {
			b.err = fmt.Errorf("count administrators: %w", err)
			return false
		}
		b.remaining = remaining
	}
	if b.err != nil || b.remaining <= 1 {
		return false
	}
	b.remaining--
	return true
}

func (s *Service) BulkSetStatus(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, status, reason string) (BulkResult, error) {
	if !slices.Contains(Statuses, status) {
		return BulkResult{}, fmt.Errorf("%w: %q is not an account status", ErrInvalidAccount, status)
	}
	// Returning to active needs no reason, and clears the old one.
	if status != StatusActive {
		checked, err := validateReason(reason)
		if err != nil {
			return BulkResult{}, err
		}
		reason = checked
	} else {
		reason = ""
	}
	if err := boundSelection(ids); err != nil {
		return BulkResult{}, err
	}

	budget := s.newAdminBudget(ctx)

	// Leaving "deleted" puts the login and email back under the partial
	// unique index, so they may collide with a live account. Asked before
	// the transaction, so a collision skips only its own account.
	taken := map[uuid.UUID]string{}
	if status != StatusDeleted {
		conflicting, err := s.repo.TakenAmong(ctx, ids)
		if err != nil {
			return BulkResult{}, err
		}
		for _, c := range conflicting {
			// When both collide, report the login.
			switch {
			case c.Login:
				taken[c.ID] = SkipLoginTaken
			case c.Email:
				taken[c.ID] = SkipEmailTaken
			}
		}
	}

	// Two deleted rows may share a login, and leaving "deleted" together
	// they would collide with each other, which TakenAmong cannot see. So a
	// login or email claimed by an earlier account in this selection counts
	// as taken; the first id named wins.
	reclaimedLogins := map[string]bool{}
	reclaimedEmails := map[string]bool{}

	sel, err := s.classify(ctx, ids, func(u User) string {
		switch {
		case u.ID == actorID:
			return SkipSelf
		// A no-op only when the reason is unchanged too: a new reason for the
		// same status must still land.
		case u.Status == status && u.StatusReason == reason:
			return SkipAlreadyInStatus
		case taken[u.ID] != "":
			return taken[u.ID]
		}
		releasesIndex := status != StatusDeleted && u.Status == StatusDeleted
		var loginKey string
		if releasesIndex {
			loginKey = strings.ToLower(u.Login)
			switch {
			case reclaimedLogins[loginKey]:
				return SkipLoginTaken
			case u.Email != "" && reclaimedEmails[u.Email]:
				return SkipEmailTaken
			}
		}
		if holdsAdmin(u.Roles) && u.IsActive() && !budget.spend() {
			return SkipLastAdministrator
		}
		if releasesIndex {
			reclaimedLogins[loginKey] = true
			// An empty email never collides.
			if u.Email != "" {
				reclaimedEmails[u.Email] = true
			}
		}
		return ""
	})
	if err != nil {
		return BulkResult{}, err
	}
	if budget.err != nil {
		return BulkResult{}, budget.err
	}
	if len(sel.accepted) == 0 {
		return BulkResult{Skipped: sel.skipped}, nil
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		entries[i] = s.entry(actorID, statusAction(u.Status, status), u.ID,
			map[string]any{"from": u.Status, "reason": reason})
	}

	change := StatusChange{Reason: reason, By: actorID, At: time.Now()}
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetStatus(ctx, changed, status, change); err != nil {
			return err
		}
		// Blocking or deleting ends open sessions.
		if status != StatusActive {
			if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
				return err
			}
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkResult{}, err
	}
	// Every move, including to active.
	s.forget(ctx, changed...)
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
}

// BulkReplaceRoles sets the same roles on a selection of accounts, with the
// same last-administrator guard as ReplaceRoles.
func (s *Service) BulkReplaceRoles(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, roleCodes []string) (BulkResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkResult{}, err
	}
	budget := s.newAdminBudget(ctx)
	keepsAdmin := slices.Contains(roleCodes, RoleAdmin)

	sel, err := s.classify(ctx, ids, func(u User) string {
		// Also keeps a deleted account off the administrator budget.
		if u.Status == StatusDeleted {
			return SkipDeleted
		}
		if holdsAdmin(u.Roles) && u.IsActive() && !keepsAdmin && !budget.spend() {
			return SkipLastAdministrator
		}
		return ""
	})
	if err != nil {
		return BulkResult{}, err
	}
	if budget.err != nil {
		return BulkResult{}, budget.err
	}
	if len(sel.accepted) == 0 {
		return BulkResult{Skipped: sel.skipped}, nil
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		changes := audit.NewChanges()
		changes.Set("roles", u.Roles, roleCodes)
		entries[i] = s.entry(actorID, audit.ActionUserRolesChange, u.ID, changes.Payload())
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceRolesMany(ctx, changed, roleCodes); err != nil {
			return err
		}
		// A demotion must bite now, not at the next login.
		if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
			return err
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkResult{}, err
	}
	s.forget(ctx, changed...)
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
}

type IssuedPassword struct {
	ID              uuid.UUID
	Login           string
	OneTimePassword string
}

type BulkPasswordResult struct {
	Issued  []IssuedPassword
	Skipped []SkippedAccount
}

// hashWorkers bounds the parallel hashing: argon2id is expensive in time and
// memory.
const hashWorkers = 3

// BulkResetPassword issues each selected account its own one-time password.
// Hashing runs on bounded workers before the transaction; a hashing failure
// aborts the whole operation rather than becoming a skip (CLAUDE.md rule 8).
func (s *Service) BulkResetPassword(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID) (BulkPasswordResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkPasswordResult{}, err
	}

	sel, err := s.classify(ctx, ids, func(u User) string {
		if u.Status == StatusDeleted {
			return SkipDeleted
		}
		return ""
	})
	if err != nil {
		return BulkPasswordResult{}, err
	}
	if len(sel.accepted) == 0 {
		return BulkPasswordResult{Skipped: sel.skipped}, nil
	}

	issued := make([]IssuedPassword, len(sel.accepted))
	creds := make([]Credential, len(sel.accepted))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(hashWorkers)
	for i, u := range sel.accepted {
		group.Go(func() error {
			if groupCtx.Err() != nil {
				return groupCtx.Err()
			}
			oneTime, err := generatePassword()
			if err != nil {
				return err
			}
			hash, err := s.issuing.Hash(groupCtx, oneTime)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			// Each goroutine writes only its own index.
			issued[i] = IssuedPassword{ID: u.ID, Login: u.Login, OneTimePassword: oneTime}
			creds[i] = Credential{UserID: u.ID, Hash: hash}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return BulkPasswordResult{}, err
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		entries[i] = s.entry(actorID, audit.ActionUserPasswordReset, u.ID, nil)
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPasswordMany(ctx, creds); err != nil {
			return err
		}
		if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
			return err
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkPasswordResult{}, err
	}
	s.forget(ctx, changed...)
	return BulkPasswordResult{Issued: issued, Skipped: sel.skipped}, nil
}

func boundSelection(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: no accounts were selected", ErrInvalidAccount)
	}
	if len(ids) > MaxBulkAccounts {
		return fmt.Errorf("%w: %d accounts, the most in one operation is %d",
			ErrTooManyAccounts, len(ids), MaxBulkAccounts)
	}
	return nil
}

// statusAction names the audit action of a move. It takes both ends, since a
// return to active is an unblock or a restore.
func statusAction(from, to string) string {
	switch {
	case to == StatusBlocked:
		return audit.ActionUserBlock
	case to == StatusDeleted:
		return audit.ActionUserDelete
	case from == StatusDeleted:
		return audit.ActionUserRestore
	default:
		return audit.ActionUserUnblock
	}
}
