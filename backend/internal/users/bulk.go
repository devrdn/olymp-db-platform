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
	// SkipDeleted is unused by BulkSetStatus, which is expected to act on a
	// deleted account (moving it to blocked or back to active). It belongs to
	// the bulk operations that must not: bulk role changes and bulk password
	// resets skip a deleted account rather than touch it.
	SkipDeleted = "deleted"
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
// This is the first of the two phases every bulk operation runs, and the
// split is not an optimisation but a correctness requirement. Deciding who
// may change has to see the selection as a whole: refuseIfLastAdmin asks
// storage how many administrators remain, so a loop that called it per
// account would let a selection holding the last two administrators through
// — each call sees the other one still standing.
//
// So this phase resolves the selection with one read and decides everything
// in memory, spending a single administrator budget as it goes (see
// adminBudget); the caller then applies the survivors in one transaction
// with one statement per kind of write. A selection of five hundred costs a
// handful of queries rather than a couple of thousand.
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
// It asks storage for the count on its first spend rather than when
// constructed, so a selection that never names an active administrator never
// spends the query — deciding stays cheap for the common case of a selection
// that turns out to be entirely not_found or already_in_status. Once asked,
// though, the count is still taken exactly once for the whole selection: a
// second spend reuses it rather than asking again, which is the defect a
// per-account loop has and this design does not.
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

// spend takes one administrator away, or refuses because it is the last. A
// failure fetching the count is remembered on the budget rather than
// returned here — decide has no way to report an error — and BulkSetStatus
// checks it once classify has finished.
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

	budget := s.newAdminBudget(ctx)

	// Moving a deleted account to any other status — active or blocked alike
	// — releases the partial unique index's hold on its login and email
	// (the index is WHERE status <> 'deleted'), so both destinations can
	// collide with a live account that has since reclaimed one. Asked once,
	// before the transaction, so one collision skips its own account instead
	// of failing the whole operation.
	taken := map[uuid.UUID]string{}
	if status != StatusDeleted {
		conflicting, err := s.repo.TakenAmong(ctx, ids)
		if err != nil {
			return BulkResult{}, err
		}
		for _, c := range conflicting {
			// Both may collide; the login is what an administrator searches
			// by, so it is the one reported.
			switch {
			case c.Login:
				taken[c.ID] = SkipLoginTaken
			case c.Email:
				taken[c.ID] = SkipEmailTaken
			}
		}
	}

	// TakenAmong only ever compares a deleted account in the selection
	// against a LIVE one — the partial index has nothing to say about two
	// deleted rows sharing a login, since both are exempt from it today. But
	// the moment two such rows leave "deleted" in the same batch, the second
	// one to land collides with the first: "delete ivanov, create ivanov,
	// delete again" produces exactly that pair. So the accepted set is
	// folded against itself as it is built: a login or email already claimed
	// by an earlier account in this same selection is taken exactly as if a
	// live account held it. The ids are visited in the order the caller gave
	// them, so the first one named wins and the outcome does not depend on
	// map iteration order.
	reclaimedLogins := map[string]bool{}
	reclaimedEmails := map[string]bool{}

	sel, err := s.classify(ctx, ids, func(u User) string {
		switch {
		case u.ID == actorID:
			return SkipSelf
		// Already in the target status is only a no-op when the reason is
		// unchanged too: the reason is part of what the operation sets, and an
		// administrator giving a new one for an account already in that status
		// (re-blocking with stronger evidence, say) is a decision that has to
		// land, not a status transition that has to happen. A move to active
		// always forces reason to "", so an already-active account compares
		// empty to empty here and stays a no-op.
		case u.Status == status && u.StatusReason == reason:
			return SkipAlreadyInStatus
		case taken[u.ID] != "":
			return taken[u.ID]
		}
		// Only an account actually leaving "deleted" reclaims anything; one
		// already skipped above never gets here, so it never blocks another.
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
		// Only an account that can administer today is one to protect, and
		// only a status that cannot administer takes it away.
		if holdsAdmin(u.Roles) && u.IsActive() && !budget.spend() {
			return SkipLastAdministrator
		}
		if releasesIndex {
			reclaimedLogins[loginKey] = true
			// An empty email is "no email", not a value every empty account
			// shares — it must never collide with another empty one.
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

// BulkReplaceRoles sets the same roles on a selection of accounts.
//
// The demotion guard mirrors the single-account ReplaceRoles: the budget is
// only at risk when the new role set does not keep the administrator role,
// and only an account that can administer today is one to protect.
func (s *Service) BulkReplaceRoles(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, roleCodes []string) (BulkResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkResult{}, err
	}
	// Lazy, exactly as BulkSetStatus's: a selection that turns out to hold no
	// active administrator never spends the count query.
	budget := s.newAdminBudget(ctx)
	keepsAdmin := slices.Contains(roleCodes, RoleAdmin)

	sel, err := s.classify(ctx, ids, func(u User) string {
		// A deleted account cannot sign in, so giving it roles is pointless —
		// and it must not spend the administrator budget either.
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
		// New limits have to bite immediately: a demotion that waited for the
		// next login would leave someone exercising rights they no longer hold.
		if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
			return err
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkResult{}, err
	}
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
}

// IssuedPassword is one account's new one-time password, to be handed over.
type IssuedPassword struct {
	ID    uuid.UUID
	Login string
	// OneTimePassword is shown once and never stored in the clear.
	OneTimePassword string
}

// BulkPasswordResult reports the passwords issued and the accounts skipped.
type BulkPasswordResult struct {
	Issued  []IssuedPassword
	Skipped []SkippedAccount
}

// hashWorkers bounds the parallel hashing.
//
// argon2id is deliberately expensive — tens of milliseconds and a large
// buffer per call — so five hundred of them in sequence is most of a minute,
// and five hundred at once is a memory spike an administrator can trigger
// from a form. The same reasoning, and the same number, as
// provisioning.DefaultWorkers.
const hashWorkers = 3

// BulkResetPassword issues a new one-time password per selected account.
//
// Every account gets its own password: one password issued to a group would
// be one password to share. Hashing runs on a bounded pool of workers ahead
// of the transaction, because it is the expensive part; a failure there —
// the machine, not the account — aborts the whole operation rather than
// turning into a skipped row, matching CLAUDE.md's rule that only a
// row-level reason from the closed skip vocabulary may become a skip.
func (s *Service) BulkResetPassword(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID) (BulkPasswordResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkPasswordResult{}, err
	}

	sel, err := s.classify(ctx, ids, func(u User) string {
		// A deleted account cannot sign in, so a new password for it is
		// pointless.
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
			// Each goroutine writes only its own index, so this needs no lock.
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
