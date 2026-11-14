package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests"
	"github.com/devrdn/olymp-db-platform/backend/internal/contests/conteststest"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
)

func TestAManagerIsAppointedToTheContest(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager)
	if err != nil {
		t.Fatalf("GrantManager() = %v", err)
	}

	staff, err := f.Service.Managers(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("Managers() = %v", err)
	}
	for _, m := range staff {
		if m.UserID == assistant.ID && m.Role == rbac.RoleManager {
			return
		}
	}
	t.Errorf("staff = %+v, want the assistant as a manager", staff)
}

// A session opened before the appointment was opened for somebody who did
// not act as staff, through whichever provider admitted them then.
func TestAnAppointmentRetiresTheAppointeesSessions(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")
	var lockedInTransaction bool
	f.Users.OnLock = func(id uuid.UUID) { lockedInTransaction = id == assistant.ID && f.UnitOfWork.Open }

	if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager); err != nil {
		t.Fatalf("GrantManager() = %v", err)
	}

	if !lockedInTransaction {
		t.Error("the appointee's account was not locked inside the appointment's transaction")
	}
	stored, _ := f.Users.ByID(context.Background(), assistant.ID)
	if stored.SessionGeneration != assistant.SessionGeneration+1 {
		t.Errorf("session generation = %d, want %d", stored.SessionGeneration, assistant.SessionGeneration+1)
	}
	if len(f.Access.Forgotten) != 1 || f.Access.Forgotten[0] != assistant.ID {
		t.Errorf("forgotten = %v, want the appointee", f.Access.Forgotten)
	}
	if f.Access.DuringTransaction != 0 {
		t.Error("the access cache was told before the appointment committed, so a request could cache the old row again")
	}
}

// An appointment that gives no new staff power must not sign the appointee
// out, an administrator appointing themselves included.
func TestAppointingAnAccountThatAlreadyActsAsStaffKeepsItsSessions(t *testing.T) {
	for name, arrange := range map[string]func(f *conteststest.Fixture, c contests.Contest) users.User{
		"a global role": func(f *conteststest.Fixture, _ contests.Contest) users.User {
			return f.AddAdministrator("admin")
		},
		"a manager of another contest": func(f *conteststest.Fixture, _ contests.Contest) users.User {
			other := f.SeedContest(contests.StatusDraft)
			u := f.AddUser("assistant")
			if err := f.Service.GrantManager(context.Background(), uuid.New(), other.ID, u.ID, rbac.RoleManager); err != nil {
				t.Fatalf("first GrantManager() = %v", err)
			}
			u, _ = f.Users.ByID(context.Background(), u.ID)
			return u
		},
		"a manager of this contest, appointed again": func(f *conteststest.Fixture, c contests.Contest) users.User {
			u := f.AddUser("assistant")
			if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, u.ID, rbac.RoleManager); err != nil {
				t.Fatalf("first GrantManager() = %v", err)
			}
			u, _ = f.Users.ByID(context.Background(), u.ID)
			return u
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := conteststest.NewFixture()
			c := f.SeedContest(contests.StatusDraft)
			staff := arrange(f, c)
			f.Access.Forgotten = nil

			if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, staff.ID, rbac.RoleManager); err != nil {
				t.Fatalf("GrantManager() = %v", err)
			}

			if stored, _ := f.Users.ByID(context.Background(), staff.ID); stored.SessionGeneration != staff.SessionGeneration {
				t.Errorf("session generation = %d, want %d kept", stored.SessionGeneration, staff.SessionGeneration)
			}
			if len(f.Access.Forgotten) != 0 {
				t.Errorf("forgotten = %v, want nothing", f.Access.Forgotten)
			}
		})
	}
}

func TestARefusedAppointmentRetiresNoSession(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")
	if _, err := f.Registrations.Add(context.Background(), c.ID, student.ID); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, student.ID, rbac.RoleManager); !errors.Is(err, contests.ErrParticipantCannotBeStaff) {
		t.Fatalf("GrantManager() = %v, want ErrParticipantCannotBeStaff", err)
	}

	if stored, _ := f.Users.ByID(context.Background(), student.ID); stored.SessionGeneration != student.SessionGeneration {
		t.Errorf("session generation = %d, want it unchanged", stored.SessionGeneration)
	}
	if len(f.Access.Forgotten) != 0 {
		t.Errorf("forgotten = %v, want nothing", f.Access.Forgotten)
	}
}

func TestAppointingAnAccountThatDoesNotExistIsRefused(t *testing.T) {
	// The foreign key would refuse it too, but as an opaque 500.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, uuid.New(), rbac.RoleManager)

	if !errors.Is(err, users.ErrNotFound) {
		t.Errorf("GrantManager() = %v, want users.ErrNotFound", err)
	}
}

func TestAppointingADeletedAccountIsRefused(t *testing.T) {
	// ByID still returns a deleted account (the audit trail keeps its subject),
	// and it can never sign in to act on the appointment.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	deleted := f.Users.Add(users.User{Login: "gone", FullName: "gone", Status: users.StatusDeleted})

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, deleted.ID, rbac.RoleManager)

	if !errors.Is(err, users.ErrAccountDeleted) {
		t.Errorf("GrantManager() = %v, want users.ErrAccountDeleted", err)
	}
}

func TestAppointingABlockedAccountIsRefused(t *testing.T) {
	// A blocked account can never sign in to act on the appointment.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	blocked := f.Users.Add(users.User{Login: "blocked", FullName: "blocked", Status: users.StatusBlocked})

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, blocked.ID, rbac.RoleManager)

	if !errors.Is(err, users.ErrAccountBlocked) {
		t.Errorf("GrantManager() = %v, want users.ErrAccountBlocked", err)
	}
}

// Staff see the reference answers and the unfrozen leaderboard.
func TestGrantManagerRefusesARegisteredParticipant(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusPublished)
	student := f.AddUser("s.popescu")
	if _, err := f.Registrations.Add(context.Background(), c.ID, student.ID); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, student.ID, rbac.RoleManager)

	if !errors.Is(err, contests.ErrParticipantCannotBeStaff) {
		t.Errorf("GrantManager() = %v, want ErrParticipantCannotBeStaff", err)
	}
	if _, err := f.Managers.Get(context.Background(), c.ID, student.ID); !errors.Is(err, contests.ErrManagerNotFound) {
		t.Errorf("a refused appointment must not staff the contest: Get() = %v", err)
	}
}

func TestOwnershipCannotBeHandedOverThroughTheStaffList(t *testing.T) {
	// Two owners would make "who may appoint staff" ambiguous.
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleOwner)

	if !errors.Is(err, contests.ErrOwnerImmutable) {
		t.Errorf("GrantManager(owner) = %v, want ErrOwnerImmutable", err)
	}
}

func TestTheOwnerCannotBeRemovedFromTheStaff(t *testing.T) {
	// A contest with no owner has nobody who may appoint anybody.
	f := conteststest.NewFixture()
	author := f.AddUser("author")
	created, err := f.Service.Create(context.Background(), contests.CreateCommand{ActorID: author.ID})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	err = f.Service.RevokeManager(context.Background(), uuid.New(), created.ID, author.ID)

	if !errors.Is(err, contests.ErrOwnerImmutable) {
		t.Errorf("RevokeManager(owner) = %v, want ErrOwnerImmutable", err)
	}
}

func TestAnUnknownContestRoleIsRefused(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, "editor")

	if !errors.Is(err, contests.ErrInvalidRole) {
		t.Errorf("GrantManager(editor) = %v, want ErrInvalidRole", err)
	}
}

func TestRevokingAManagerRemovesThem(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")
	if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager); err != nil {
		t.Fatalf("GrantManager() = %v", err)
	}

	if err := f.Service.RevokeManager(context.Background(), uuid.New(), c.ID, assistant.ID); err != nil {
		t.Fatalf("RevokeManager() = %v", err)
	}

	if _, err := f.Managers.Get(context.Background(), c.ID, assistant.ID); !errors.Is(err, contests.ErrManagerNotFound) {
		t.Errorf("the assistant is still staff: %v", err)
	}
}

func TestRevokingAManagerRemembersThem(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")
	if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager); err != nil {
		t.Fatalf("GrantManager() = %v", err)
	}

	if err := f.Service.RevokeManager(context.Background(), uuid.New(), c.ID, assistant.ID); err != nil {
		t.Fatalf("RevokeManager() = %v", err)
	}

	former, err := f.Managers.FormerManagers(context.Background(), c.ID)
	if err != nil {
		t.Fatalf("FormerManagers() = %v", err)
	}
	if len(former) != 1 || former[0] != assistant.ID {
		t.Errorf("FormerManagers() = %v, want just the assistant", former)
	}
}

func TestRevokingSomebodyWhoIsNotStaffReportsNotFound(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	stranger := f.AddUser("stranger")

	err := f.Service.RevokeManager(context.Background(), uuid.New(), c.ID, stranger.ID)

	if !errors.Is(err, contests.ErrManagerNotFound) {
		t.Errorf("RevokeManager() = %v, want ErrManagerNotFound", err)
	}
}

func TestAppointmentsAreAudited(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusDraft)
	assistant := f.AddUser("assistant")

	if err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager); err != nil {
		t.Fatalf("GrantManager() = %v", err)
	}

	if !f.Audit.Recorded(audit.ActionManagerGrant) {
		t.Errorf("audit entries = %v, want a %s", f.Audit.Actions(), audit.ActionManagerGrant)
	}
}

func TestStaffCannotChangeOnceTheContestIsArchived(t *testing.T) {
	f := conteststest.NewFixture()
	c := f.SeedContest(contests.StatusArchived)
	assistant := f.AddUser("assistant")

	err := f.Service.GrantManager(context.Background(), uuid.New(), c.ID, assistant.ID, rbac.RoleManager)

	if !errors.Is(err, contests.ErrNotEditable) {
		t.Errorf("GrantManager() on an archived contest = %v, want ErrNotEditable", err)
	}
}
