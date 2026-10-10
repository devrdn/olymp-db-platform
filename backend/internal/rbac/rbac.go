// Package rbac answers one question: may this identity perform this action,
// here?
//
// Global roles (`user_roles`) grant installation-wide abilities such as
// creating contests or managing accounts. Contest roles (`contest_managers`)
// grant power over one contest and nothing else: running one contest gives no
// say over another, and never the ability to manage accounts.
//
// It does not authenticate (auth) and does not store roles (users).
package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Permission codes, mirroring the rows seeded into `permissions`, so a typo
// is a compile error rather than a silent denial.
const (
	PermissionContestCreate     = "contest.create"
	PermissionContestView       = "contest.view"
	PermissionContestEdit       = "contest.edit"
	PermissionContestPublish    = "contest.publish"
	PermissionContestManage     = "contest.manage"
	PermissionParticipantManage = "participant.manage"
	PermissionReportsView       = "reports.view"
	PermissionUsersManage       = "users.manage"
	PermissionAuditView         = "audit.view"
	PermissionSettingsManage    = "settings.manage"

	// PermissionContestMonitor lets its holder watch everything a contest's
	// participants did. It is granted wherever contest.view is, and only ever
	// checked with a contest id: the global grant to organizers must never
	// gate an installation-wide view of every contest's participants.
	PermissionContestMonitor = "contest.monitor"

	// PermissionContestAdminAll lifts the contest scope: its holder acts on
	// every contest without being listed as a manager. Being a permission, it
	// can be given to a new role from data.
	PermissionContestAdminAll = "contest.admin_all"
)

// ContestRole is a person's standing within one contest.
type ContestRole string

const (
	RoleNone ContestRole = ""
	// RoleOwner is the contest's creator: everything a manager may do, plus
	// appointing managers (contest.manage). Archiving is a status change
	// under contest.publish, which managers hold too.
	RoleOwner   ContestRole = "owner"
	RoleManager ContestRole = "manager"
)

// ErrForbidden reports that the identity may not perform the action. It
// carries no reason, since the reason can itself leak information (that a
// contest exists, for instance).
var ErrForbidden = errors.New("forbidden")

var managerPermissions = map[string]struct{}{
	PermissionContestView:       {},
	PermissionContestMonitor:    {},
	PermissionContestEdit:       {},
	PermissionContestPublish:    {},
	PermissionParticipantManage: {},
	PermissionReportsView:       {},
}

// ownerOnlyPermissions are reserved for the contest owner, so a manager cannot
// appoint more staff.
var ownerOnlyPermissions = map[string]struct{}{
	PermissionContestManage: {},
}

// Identity is an authenticated user together with the permissions their global
// roles grant.
type Identity struct {
	UserID      uuid.UUID
	Login       string
	Permissions map[string]struct{}
}

func (i Identity) Has(permission string) bool {
	_, ok := i.Permissions[permission]
	return ok
}

// ContestRoleLoader reads a user's standing in one contest.
type ContestRoleLoader interface {
	ContestRole(ctx context.Context, userID, contestID uuid.UUID) (ContestRole, error)
}

type Authorizer struct {
	roles ContestRoleLoader
}

func New(loader ContestRoleLoader) *Authorizer {
	return &Authorizer{roles: loader}
}

// Authorize reports whether the identity may exercise the permission. Pass
// uuid.Nil as contestID for installation-wide actions.
//
// It returns ErrForbidden on denial, and a wrapped error when the decision
// could not be made; a caller must treat the latter as a failure, never as
// permission.
func (a *Authorizer) Authorize(ctx context.Context, id Identity, permission string, contestID uuid.UUID) error {
	if contestID == uuid.Nil {
		if id.Has(permission) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrForbidden, permission)
	}

	// admin_all skips the contest-role lookup. It sits after the
	// installation-wide branch because it lifts the contest scope only and
	// must not double as users.manage or audit.view.
	if id.Has(PermissionContestAdminAll) {
		return nil
	}

	role, err := a.roles.ContestRole(ctx, id.UserID, contestID)
	if err != nil {
		return fmt.Errorf("resolve contest role: %w", err)
	}

	if grantsContestPermission(role, permission) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrForbidden, permission)
}

func grantsContestPermission(role ContestRole, permission string) bool {
	switch role {
	case RoleOwner:
		if _, ok := ownerOnlyPermissions[permission]; ok {
			return true
		}
		_, ok := managerPermissions[permission]
		return ok
	case RoleManager:
		_, ok := managerPermissions[permission]
		return ok
	default:
		return false
	}
}
