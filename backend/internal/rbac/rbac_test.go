package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

var (
	contestA = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	contestB = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002")

	alice = uuid.MustParse("11111111-1111-1111-1111-111111111111")
)

// fakeRoles answers contest-role lookups from a fixed table.
type fakeRoles struct {
	roles map[uuid.UUID]ContestRole
	err   error
	calls int
}

func (f *fakeRoles) ContestRole(_ context.Context, _ uuid.UUID, contestID uuid.UUID) (ContestRole, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.roles[contestID], nil
}

func identity(permissions ...string) Identity {
	set := make(map[string]struct{}, len(permissions))
	for _, p := range permissions {
		set[p] = struct{}{}
	}
	return Identity{UserID: alice, Login: "alice", Permissions: set}
}

func TestUnscopedPermissionIsGrantedByAGlobalRole(t *testing.T) {
	auth := New(&fakeRoles{})

	err := auth.Authorize(context.Background(), identity(PermissionContestCreate), PermissionContestCreate, uuid.Nil)

	if err != nil {
		t.Errorf("Authorize() = %v, want nil", err)
	}
}

func TestUnscopedPermissionIsDeniedWithoutIt(t *testing.T) {
	auth := New(&fakeRoles{})

	err := auth.Authorize(context.Background(), identity(PermissionReportsView), PermissionUsersManage, uuid.Nil)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want ErrForbidden", err)
	}
}

func TestContestManagerActsOnTheirOwnContest(t *testing.T) {
	auth := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleManager}})

	err := auth.Authorize(context.Background(), identity(), PermissionContestEdit, contestA)

	if err != nil {
		t.Errorf("Authorize() = %v, want the manager of this contest to be allowed", err)
	}
}

func TestContestManagerCannotActOnSomebodyElsesContest(t *testing.T) {
	// The whole point of the second level: managing one contest grants nothing
	// anywhere else.
	auth := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleManager}})

	err := auth.Authorize(context.Background(), identity(), PermissionContestEdit, contestB)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want ErrForbidden for a contest the user does not manage", err)
	}
}

func TestGlobalPermissionDoesNotByItselfUnlockEveryContest(t *testing.T) {
	// An organizer holds contest.edit globally, but that means "may edit the
	// contests they run", not "may edit anyone's contest".
	auth := New(&fakeRoles{})

	err := auth.Authorize(context.Background(), identity(PermissionContestEdit), PermissionContestEdit, contestA)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want a global grant to be insufficient without a contest role", err)
	}
}

func TestSystemAdministratorPassesEveryContestScopedCheck(t *testing.T) {
	roles := &fakeRoles{}
	auth := New(roles)

	err := auth.Authorize(context.Background(), identity(PermissionContestAdminAll), PermissionContestEdit, contestA)

	if err != nil {
		t.Errorf("Authorize() = %v, want a system administrator to be allowed", err)
	}
	if roles.calls != 0 {
		t.Error("a contest-role lookup was made for a system administrator; the shortcut should skip it")
	}
}

func TestContestAdminAllLiftsOnlyTheContestScope(t *testing.T) {
	// The permission's own comment says what it does: "acts on every contest
	// without being listed as a manager". It says nothing about accounts or
	// the audit trail, and the package doc is explicit that contest power
	// "must never gain the ability to manage accounts". A shortcut that fires
	// before the scope is even looked at would hand an auditor role, granted
	// admin_all from data alone, every installation-wide right there is —
	// including resetting any account's password.
	auth := New(&fakeRoles{})

	err := auth.Authorize(context.Background(), identity(PermissionContestAdminAll), PermissionUsersManage, uuid.Nil)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want admin_all to be worthless outside a contest scope", err)
	}
}

func TestOnlyTheOwnerMayAppointManagers(t *testing.T) {
	owner := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleOwner}})
	manager := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleManager}})
	ctx := context.Background()

	if err := owner.Authorize(ctx, identity(), PermissionContestManage, contestA); err != nil {
		t.Errorf("owner was denied contest.manage: %v", err)
	}
	if err := manager.Authorize(ctx, identity(), PermissionContestManage, contestA); !errors.Is(err, ErrForbidden) {
		t.Errorf("manager was allowed to appoint managers: %v", err)
	}
}

func TestManagerHoldsTheEverydayContestPermissions(t *testing.T) {
	auth := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleManager}})
	ctx := context.Background()

	for _, permission := range []string{
		PermissionContestView,
		PermissionContestEdit,
		PermissionContestPublish,
		PermissionParticipantManage,
		PermissionReportsView,
	} {
		if err := auth.Authorize(ctx, identity(), permission, contestA); err != nil {
			t.Errorf("manager was denied %s: %v", permission, err)
		}
	}
}

func TestContestRoleGrantsNothingOutsideTheContest(t *testing.T) {
	// Running a contest must not turn into managing the installation.
	auth := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{contestA: RoleOwner}})

	err := auth.Authorize(context.Background(), identity(), PermissionUsersManage, contestA)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want a contest owner to be denied account management", err)
	}
}

func TestUserWithNoRoleInTheContestIsDenied(t *testing.T) {
	auth := New(&fakeRoles{roles: map[uuid.UUID]ContestRole{}})

	err := auth.Authorize(context.Background(), identity(), PermissionContestView, contestA)

	if !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize() = %v, want ErrForbidden", err)
	}
}

func TestLookupFailureDeniesRatherThanAllows(t *testing.T) {
	// A database blip must not become an authorisation bypass.
	wantErr := errors.New("connection reset")
	auth := New(&fakeRoles{err: wantErr})

	err := auth.Authorize(context.Background(), identity(), PermissionContestEdit, contestA)

	if err == nil {
		t.Fatal("Authorize() allowed the action although the role lookup failed")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap the lookup failure", err)
	}
}

func TestHasReportsGlobalPermissions(t *testing.T) {
	id := identity(PermissionContestCreate, PermissionReportsView)

	if !id.Has(PermissionContestCreate) {
		t.Error("Has() missed a granted permission")
	}
	if id.Has(PermissionUsersManage) {
		t.Error("Has() reported a permission that was never granted")
	}
}
