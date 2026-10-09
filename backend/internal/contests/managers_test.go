package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
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
