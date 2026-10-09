package contests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Errors about contest staff.
var (
	ErrManagerNotFound = errors.New("this user does not staff the contest")
	// ErrOwnerImmutable refuses changing or removing the owner here: a contest
	// with no owner has nobody who can appoint staff, and one with two owners
	// is ambiguous.
	ErrOwnerImmutable = errors.New("the contest owner cannot be changed here")
	ErrInvalidRole    = errors.New("unknown contest role")
	// ErrParticipantCannotBeStaff refuses to appoint a participant of the
	// contest: staff can read the reference answers and the unfrozen
	// leaderboard. ErrStaffCannotParticipate is the opposite direction.
	ErrParticipantCannotBeStaff = errors.New("a participant cannot be appointed to the contest staff")
)

// ownerRole is the role a contest's author is appointed to.
const ownerRole = rbac.RoleOwner

// Manager is one person on a contest's staff. Roles come from internal/rbac.
type Manager struct {
	ContestID uuid.UUID
	UserID    uuid.UUID
	// Login and FullName are for display in the staff list.
	Login     string
	FullName  string
	Role      rbac.ContestRole
	GrantedBy uuid.UUID
	GrantedAt time.Time
}

// ManagerRepository stores contest staff.
type ManagerRepository interface {
	// List returns the contest's staff, owner first.
	List(ctx context.Context, contestID uuid.UUID) ([]Manager, error)
	// Get returns one staff entry, or ErrManagerNotFound.
	Get(ctx context.Context, contestID, userID uuid.UUID) (Manager, error)
	// Grant appoints a user, replacing any role they already held.
	Grant(ctx context.Context, m Manager) error
	Revoke(ctx context.Context, contestID, userID uuid.UUID) error
}

// Managers returns the contest's staff, owner first.
func (s *Service) Managers(ctx context.Context, contestID uuid.UUID) ([]Manager, error) {
	return s.managers.List(ctx, contestID)
}

// GrantManager appoints somebody to help run a contest. It never grants
// ownership: a transfer must not happen quietly through the staff list.
func (s *Service) GrantManager(ctx context.Context, actorID, contestID, userID uuid.UUID, role rbac.ContestRole) error {
	if role == rbac.RoleOwner {
		return ErrOwnerImmutable
	}
	if role != rbac.RoleManager {
		return fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	c, err := s.mutableContest(ctx, contestID)
	if err != nil {
		return err
	}
	// Checked here so an unknown account is a clear error, not an opaque
	// foreign-key violation.
	user, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	// ByID still returns deleted accounts (the audit trail keeps them). A
	// deleted or blocked account can never sign in, so appointing one would
	// staff the contest with somebody who can never act.
	if user.Status == users.StatusDeleted {
		return users.ErrAccountDeleted
	}
	if user.Status == users.StatusBlocked {
		return users.ErrAccountBlocked
	}
	// Overwriting the owner's own row would demote them by another route.
	if existing, err := s.managers.Get(ctx, contestID, userID); err == nil && existing.Role == rbac.RoleOwner {
		return ErrOwnerImmutable
	} else if err != nil && !errors.Is(err, ErrManagerNotFound) {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		// The roster check reads a different table than the write, so a
		// concurrent Enroll could pass its own staff check at the same time.
		// The contest lock serialises the two; the check must stay inside it.
		if err := s.contests.LockContest(ctx, c.ID); err != nil {
			return err
		}
		if _, err := s.registrations.ByUser(ctx, contestID, userID); err == nil {
			return ErrParticipantCannotBeStaff
		} else if !errors.Is(err, ErrParticipantNotFound) {
			return err
		}
		if err := s.managers.Grant(ctx, Manager{
			ContestID: c.ID,
			UserID:    user.ID,
			Role:      role,
			GrantedBy: actorID,
			GrantedAt: s.now(),
		}); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionManagerGrant, contestID, map[string]any{
			"user_id": user.ID.String(),
			"login":   user.Login,
			"role":    string(role),
		})
	})
}

// RevokeManager removes somebody from a contest's staff.
func (s *Service) RevokeManager(ctx context.Context, actorID, contestID, userID uuid.UUID) error {
	if _, err := s.mutableContest(ctx, contestID); err != nil {
		return err
	}
	existing, err := s.managers.Get(ctx, contestID, userID)
	if err != nil {
		return err
	}
	// A contest with no owner has nobody who may appoint anybody.
	if existing.Role == rbac.RoleOwner {
		return ErrOwnerImmutable
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.managers.Revoke(ctx, contestID, userID); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionManagerRevoke, contestID, map[string]any{
			"user_id": userID.String(),
		})
	})
}

// mutableContest loads a contest that is not archived. Staff and roster
// changes stay allowed after the finish; an archived contest is a closed
// record.
func (s *Service) mutableContest(ctx context.Context, contestID uuid.UUID) (Contest, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if c.Status == StatusArchived {
		return Contest{}, fmt.Errorf("%w: it is archived", ErrNotEditable)
	}
	return c, nil
}
