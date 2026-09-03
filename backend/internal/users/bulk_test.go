package users_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// bulkFixture extends fixture with a stored account for the actor performing
// bulk operations. A selection can legitimately name the actor among the
// accounts it targets, and the operation must recognise that as "self"
// rather than "not found" — which only happens when the actor resolves
// through ByIDs like any other account.
type bulkFixture struct {
	*fixture
	admin users.User
}

func newBulkFixture(t *testing.T) *bulkFixture {
	t.Helper()
	f := newFixture(t)
	admin := f.addUser(t, "admin", "some password")
	return &bulkFixture{fixture: f, admin: admin}
}

// createAdmin stores an account holding the administrator role.
func (f *bulkFixture) createAdmin(t *testing.T, login string) users.User {
	t.Helper()
	u := f.addUser(t, login, "some password")
	if err := f.repo.ReplaceRoles(context.Background(), u.ID, []string{users.RoleAdmin}); err != nil {
		t.Fatalf("ReplaceRoles() returned error: %v", err)
	}
	u.Roles = []string{users.RoleAdmin}
	return u
}

// createUser stores an ordinary account with no roles.
func (f *bulkFixture) createUser(t *testing.T, login string) users.User {
	t.Helper()
	return f.addUser(t, login, "some password")
}

// TestBulkBlockKeepsOneAdministrator guards the defect a per-account loop has
// and this design does not: refuseIfLastAdmin recounts on every call, so two
// administrators checked one after another each see the other still
// standing, and both go.
func TestBulkBlockKeepsOneAdministrator(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createAdmin(t, "admin-one")
	second := f.createAdmin(t, "admin-two")

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, users.StatusBlocked, "end of term")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Changed) != 1 {
		t.Fatalf("Changed = %v, want exactly one account", res.Changed)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want exactly one account", res.Skipped)
	}
	if res.Skipped[0].Reason != users.SkipLastAdministrator {
		t.Errorf("Skipped[0].Reason = %q, want %q", res.Skipped[0].Reason, users.SkipLastAdministrator)
	}
}

func TestBulkStatusSkipsRatherThanFails(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	missing := uuid.New()

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID, missing, f.admin.ID}, users.StatusDeleted, "graduated")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Changed) != 1 || res.Changed[0] != target.ID {
		t.Fatalf("Changed = %v, want [%v]", res.Changed, target.ID)
	}
	reasons := map[uuid.UUID]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	if reasons[missing] != users.SkipNotFound {
		t.Errorf("reason for the missing id = %q, want %q", reasons[missing], users.SkipNotFound)
	}
	if reasons[f.admin.ID] != users.SkipSelf {
		t.Errorf("reason for the actor = %q, want %q", reasons[f.admin.ID], users.SkipSelf)
	}
}

func TestBulkStatusBoundsTheSelection(t *testing.T) {
	f := newBulkFixture(t)
	ids := make([]uuid.UUID, users.MaxBulkAccounts+1)
	for i := range ids {
		ids[i] = uuid.New()
	}

	_, err := f.service.BulkSetStatus(context.Background(), f.admin.ID, ids, users.StatusBlocked, "why")

	if !errors.Is(err, users.ErrTooManyAccounts) {
		t.Errorf("err = %v, want ErrTooManyAccounts", err)
	}
}

// TestBulkStatusAppliesInOneTransaction guards the point of the two phases:
// deciding costs no transaction, and applying costs exactly one however many
// accounts survived.
func TestBulkStatusAppliesInOneTransaction(t *testing.T) {
	f := newBulkFixture(t)
	ids := []uuid.UUID{
		f.createUser(t, "a").ID,
		f.createUser(t, "b").ID,
		f.createUser(t, "c").ID,
	}

	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID, ids, users.StatusBlocked, "end of term"); err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}
	if f.uow.Calls != 1 {
		t.Errorf("uow.Calls = %d, want 1", f.uow.Calls)
	}
}

func TestBulkStatusOpensNoTransactionWhenNothingSurvives(t *testing.T) {
	f := newBulkFixture(t)

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{uuid.New()}, users.StatusBlocked, "end of term")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("Changed = %v, want none", res.Changed)
	}
	if f.uow.Calls != 0 {
		t.Errorf("uow.Calls = %d, want 0", f.uow.Calls)
	}
}
