// Package rbac answers one question: may this identity perform this action,
// here?
//
// Authorisation has two levels, and they are not interchangeable. Global roles
// (`user_roles`) grant installation-wide abilities such as creating contests or
// managing accounts. Contest roles (`contest_managers`) grant power over one
// contest and nothing else. A person who runs the March olympiad must not
// thereby gain any say over the April one, and must never gain the ability to
// manage accounts.
package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Permission codes. They mirror the rows seeded into `permissions`; the
// constants exist so a typo is a compile error rather than a silent denial.
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
	// participants did: their queries, answers, notes, tabs and the signals
	// their browsers and sessions left. Granted wherever contest.view is — by
	// migration 000033 to the global roles, and through managerPermissions
	// below to a contest's owner and managers.
	PermissionContestMonitor = "contest.monitor"

	// PermissionContestAdminAll lifts the contest scope: its holder acts on
	// every contest without being listed as a manager. It is a permission
	// rather than a hard-coded "is admin" check, so a new role (a dean's
	// office account, an auditor) can be given the same reach from data.
	PermissionContestAdminAll = "contest.admin_all"
)

// ContestRole is a person's standing within one contest.
type ContestRole string

const (
	// RoleNone means the user is not listed as staff on the contest.
	RoleNone ContestRole = ""
	// RoleOwner is the contest's creator: everything a manager may do, plus
	// appointing managers and archiving.
	RoleOwner ContestRole = "owner"
	// RoleManager runs the contest day to day.
	RoleManager ContestRole = "manager"
)

// ErrForbidden reports that the identity may not perform the action. Callers
// translate it to 403; it deliberately carries no detail about why, since the
// reason can itself be information (that a contest exists, for instance).
var ErrForbidden = errors.New("forbidden")

// managerPermissions are the contest-scoped abilities of a manager.
var managerPermissions = map[string]struct{}{
	PermissionContestView:       {},
	PermissionContestMonitor:    {},
	PermissionContestEdit:       {},
	PermissionContestPublish:    {},
	PermissionParticipantManage: {},
	PermissionReportsView:       {},
}

// ownerOnlyPermissions are reserved for the contest owner. Appointing staff is
// the one power a manager must not be able to grant themselves more of.
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

// Has reports whether a global role grants the permission.
func (i Identity) Has(permission string) bool {
	_, ok := i.Permissions[permission]
	return ok
}

// ContestRoleLoader reads a user's standing in one contest.
//
// The lookup is per contest rather than "load every contest this user staffs",
// because a request only ever concerns the contest it names.
type ContestRoleLoader interface {
	ContestRole(ctx context.Context, userID, contestID uuid.UUID) (ContestRole, error)
}

// Authorizer decides access.
type Authorizer struct {
	roles ContestRoleLoader
}

// New returns an authorizer that resolves contest roles through loader.
func New(loader ContestRoleLoader) *Authorizer {
	return &Authorizer{roles: loader}
}

// Authorize reports whether the identity may exercise the permission.
//
// Pass uuid.Nil as contestID for installation-wide actions ("create a
// contest", "manage accounts"); pass a contest id for anything that concerns
// one contest.
//
// It returns ErrForbidden on denial, and a wrapped error when the decision
// could not be made — a caller must treat the latter as a failure, never as
// permission.
func (a *Authorizer) Authorize(ctx context.Context, id Identity, permission string, contestID uuid.UUID) error {
	if contestID == uuid.Nil {
		if id.Has(permission) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrForbidden, permission)
	}

	// Installation-wide administrators skip the contest-role lookup; there is
	// no contest they are not staff on. The shortcut sits after the
	// installation-wide branch on purpose: admin_all lifts the contest scope
	// and nothing else, so it must not double as users.manage or audit.view
	// for a role that was only ever given reach over contests.
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

// grantsContestPermission reports whether a contest role covers the permission.
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
