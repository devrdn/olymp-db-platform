package contests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// Errors about contest staff.
var (
	ErrManagerNotFound = errors.New("this user does not staff the contest")
	// ErrOwnerImmutable guards the one appointment that must not be casually
	// overwritten: a contest with no owner has nobody who can appoint staff,
	// and a contest with two owners has an ambiguous one.
	ErrOwnerImmutable = errors.New("the contest owner cannot be changed here")
	ErrInvalidRole    = errors.New("unknown contest role")
)

// ownerRole is the role a contest's author is appointed to.
const ownerRole = rbac.RoleOwner

// Manager is one person on a contest's staff.
//
// The role vocabulary comes from internal/rbac, which is where what each role
// may do is decided; repeating the strings here would be two sources of truth
// for one fact.
type Manager struct {
	ContestID uuid.UUID
	UserID    uuid.UUID
	// Login and FullName are carried for the staff list, which would otherwise
	// be a page of identifiers.
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
	// Revoke removes a user from the staff.
	Revoke(ctx context.Context, contestID, userID uuid.UUID) error
}

// Managers returns the contest's staff, owner first.
func (s *Service) Managers(ctx context.Context, contestID uuid.UUID) ([]Manager, error) {
	return s.managers.List(ctx, contestID)
}

// GrantManager appoints somebody to help run a contest.
//
// Ownership is not handed over here. Two owners make "who may appoint staff"
// ambiguous, and a transfer of ownership is not something that should happen
// quietly through the staff list.
func (s *Service) GrantManager(ctx context.Context, actorID, contestID, userID uuid.UUID, role rbac.ContestRole) error {
	if role == rbac.RoleOwner {
		return ErrOwnerImmutable
	}
	if role != rbac.RoleManager {
		return fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	c, err := s.staffableContest(ctx, contestID)
	if err != nil {
		return err
	}
	// The foreign key would refuse an unknown account too, but as an opaque
	// constraint violation; whoever mistyped a name deserves to be told.
	user, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	// Overwriting the owner's own row would demote them by another route.
	if existing, err := s.managers.Get(ctx, contestID, userID); err == nil && existing.Role == rbac.RoleOwner {
		return ErrOwnerImmutable
	} else if err != nil && !errors.Is(err, ErrManagerNotFound) {
		return err
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
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
	if _, err := s.staffableContest(ctx, contestID); err != nil {
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

// staffableContest loads a contest whose staff may still change.
//
// Wider than editing content: appointing somebody to help with the reports
// after the finish is ordinary. An archived contest is a closed record, and
// nothing about it changes any more.
func (s *Service) staffableContest(ctx context.Context, contestID uuid.UUID) (Contest, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Contest{}, err
	}
	if c.Status == StatusArchived {
		return Contest{}, fmt.Errorf("%w: it is archived", ErrNotEditable)
	}
	return c, nil
}
