package users

// Bulk operations apply one action to a selection of accounts.
//
// They run in two phases, and the split is not an optimisation but a
// correctness requirement. Deciding who may change has to see the selection as
// a whole: refuseIfLastAdmin asks storage how many administrators remain, so a
// loop that called it per account would let a selection holding the last two
// administrators through — each call sees the other one still standing.
//
// So the first phase resolves the selection with one read and decides
// everything in memory, spending a single administrator budget as it goes; the
// second applies the survivors in one transaction with one statement per kind
// of write. A selection of five hundred costs a handful of queries rather than
// a couple of thousand.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// MaxBulkAccounts bounds one operation. The request body is bounded at a
// megabyte, which is thirty thousand identifiers: a bound on the body is not a
// bound on the work.
const MaxBulkAccounts = 500

// Why an account in a selection did not change. A closed vocabulary: the
// client renders each of these, and anything else aborts the operation rather
// than being reported as a row somebody has to go and fix.
const (
	SkipNotFound          = "not_found"
	SkipSelf              = "self"
	SkipLastAdministrator = "last_administrator"
	SkipAlreadyInStatus   = "already_in_status"
	SkipDeleted           = "deleted"
)

// ErrTooManyAccounts refuses a selection above MaxBulkAccounts.
var ErrTooManyAccounts = errors.New("too many accounts in one operation")

// SkippedAccount is one account the operation did not touch.
type SkippedAccount struct {
	ID uuid.UUID
	// Login identifies the account to a person reading the result, who chose
	// the selection by name and not by identifier.
	Login  string
	Reason string
}

// BulkResult reports what an operation did and what it declined to do.
type BulkResult struct {
	Changed []uuid.UUID
	Skipped []SkippedAccount
}

// selection is the outcome of the deciding phase.
type selection struct {
	accepted []User
	skipped  []SkippedAccount
}

// classify reads the selection once and asks decide about each account.
//
// decide returns "" to accept an account or one of the skip reasons. It is
// called in the order the caller gave the ids, so a budget it closes over is
// spent predictably.
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
			continue // The same account named twice is one account.
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

// adminBudget is how many administrators may still be taken away.
//
// Counted once for the whole selection, which is the difference between this
// and asking storage per account.
type adminBudget struct{ remaining int }

func (s *Service) adminBudget(ctx context.Context) (*adminBudget, error) {
	remaining, err := s.repo.CountActiveWithRole(ctx, RoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("count administrators: %w", err)
	}
	return &adminBudget{remaining: remaining}, nil
}

// spend takes one administrator away, or refuses because it is the last.
func (b *adminBudget) spend() bool {
	if b.remaining <= 1 {
		return false
	}
	b.remaining--
	return true
}

// BulkSetStatus moves a selection of accounts to one status.
func (s *Service) BulkSetStatus(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, status, reason string) (BulkResult, error) {
	if !slices.Contains(Statuses, status) {
		return BulkResult{}, fmt.Errorf("%w: %q is not an account status", ErrInvalidAccount, status)
	}
	// Returning to the ordinary state needs no justification, and an empty
	// reason is what clears the old one.
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

	budget, err := s.adminBudget(ctx)
	if err != nil {
		return BulkResult{}, err
	}

	// Restoring an account whose login a live one has taken collides with the
	// partial unique index. Asked once, before the transaction, so one
	// collision skips its own account instead of failing the whole operation.
	taken := map[uuid.UUID]bool{}
	if status == StatusActive {
		conflicting, err := s.repo.TakenAmong(ctx, ids)
		if err != nil {
			return BulkResult{}, err
		}
		for _, id := range conflicting {
			taken[id] = true
		}
	}

	sel, err := s.classify(ctx, ids, func(u User) string {
		switch {
		case u.ID == actorID:
			return SkipSelf
		case u.Status == status:
			return SkipAlreadyInStatus
		case taken[u.ID]:
			return SkipLoginTaken
		}
		// Only an account that can administer today is one to protect, and
		// only a status that cannot administer takes it away.
		if holdsAdmin(u.Roles) && u.IsActive() && !budget.spend() {
			return SkipLastAdministrator
		}
		return ""
	})
	if err != nil {
		return BulkResult{}, err
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
		// A status nobody can work in has to stop the tabs that are already
		// open, which is the situation blocking and deletion exist to end.
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
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
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

// statusAction names the audit action this move is.
//
// It takes both ends because coming back to active is two different events:
// an account returning from a block was unblocked, one returning from deletion
// was restored, and the trail has to say which.
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
