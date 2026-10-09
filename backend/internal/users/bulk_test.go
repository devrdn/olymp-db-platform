package users_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// bulkFixture stores the actor too, so a selection naming the actor resolves
// as "self" rather than "not found".
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

func (f *bulkFixture) createAdmin(t *testing.T, login string) users.User {
	t.Helper()
	u := f.addUser(t, login, "some password")
	if err := f.repo.ReplaceRoles(context.Background(), u.ID, []string{users.RoleAdmin}); err != nil {
		t.Fatalf("ReplaceRoles() returned error: %v", err)
	}
	u.Roles = []string{users.RoleAdmin}
	return u
}

func (f *bulkFixture) createUser(t *testing.T, login string) users.User {
	t.Helper()
	return f.addUser(t, login, "some password")
}

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
	// One count for the whole selection.
	if f.repo.CountActiveWithRoleCalls != 1 {
		t.Errorf("CountActiveWithRoleCalls = %d, want 1", f.repo.CountActiveWithRoleCalls)
	}
}

func TestBulkStatusNeverAsksForTheAdministratorBudgetWhenNothingCanSpendIt(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	missing := uuid.New()

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID, missing}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Changed) != 1 || res.Changed[0] != target.ID {
		t.Fatalf("Changed = %v, want [%v]", res.Changed, target.ID)
	}
	if f.repo.CountActiveWithRoleCalls != 0 {
		t.Errorf("CountActiveWithRoleCalls = %d, want 0: neither account holds admin, so the budget should never be fetched", f.repo.CountActiveWithRoleCalls)
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

// Moving a deleted account to blocked re-imposes login uniqueness just as
// restoring it does.
func TestBulkBlockSkipsADeletedAccountWhoseLoginWasReclaimed(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.createUser(t, "reclaimed")
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	f.repo.Add(users.User{Login: "reclaimed", FullName: "New Reclaimed", PasswordHash: "x"})
	other := f.createUser(t, "unrelated")

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID, other.ID}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Changed) != 1 || res.Changed[0] != other.ID {
		t.Fatalf("Changed = %v, want [%v]: the rest of the selection must still apply", res.Changed, other.ID)
	}
	reasons := map[uuid.UUID]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	if reasons[gone.ID] != users.SkipLoginTaken {
		t.Errorf("reason for the reclaimed login = %q, want %q", reasons[gone.ID], users.SkipLoginTaken)
	}
	if live, ok := f.repo.Get(other.ID); !ok || live.Status != users.StatusBlocked {
		t.Errorf("the unrelated account was not blocked: %+v, ok=%v", live, ok)
	}
}

func TestBulkBlockReportsAnEmailCollisionAsEmailTaken(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.repo.Add(users.User{
		Login: "gone-by-email", Email: "shared@example.com", FullName: "Gone", PasswordHash: "x",
	})
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	f.repo.Add(users.User{Login: "someone-else", Email: "shared@example.com", FullName: "Live", PasswordHash: "x"})

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipEmailTaken {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipEmailTaken)
	}
}

// Two deleted rows sharing a login ("delete ivanov, create ivanov, delete
// again") collide with each other, which TakenAmong cannot see.
func TestBulkBlockSkipsASecondDeletedAccountSharingAReclaimedLogin(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createUser(t, "ivanov")
	// Add does not enforce login uniqueness, so this mirrors that history.
	second := f.addUser(t, "ivanov", "some password")
	other := f.createUser(t, "unrelated")

	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID, other.ID}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	changed := map[uuid.UUID]bool{}
	for _, id := range res.Changed {
		changed[id] = true
	}
	if !changed[first.ID] || !changed[other.ID] {
		t.Fatalf("Changed = %v, want %v and %v: the first account named and the rest of the batch must still apply", res.Changed, first.ID, other.ID)
	}
	if changed[second.ID] {
		t.Fatalf("Changed = %v, the second account sharing the login must not have applied", res.Changed)
	}
	reasons := map[uuid.UUID]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	if reasons[second.ID] != users.SkipLoginTaken {
		t.Errorf("reason for the second reclaimed login = %q, want %q", reasons[second.ID], users.SkipLoginTaken)
	}
	if live, ok := f.repo.Get(second.ID); !ok || live.Status != users.StatusDeleted {
		t.Errorf("the second account must stay deleted: %+v, ok=%v", live, ok)
	}
}

func TestBulkBlockReportsLoginWhenBothLoginAndEmailCollide(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.repo.Add(users.User{
		Login: "collide", Email: "shared@example.com", FullName: "Gone", PasswordHash: "x",
	})
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	f.repo.Add(users.User{Login: "collide", Email: "shared@example.com", FullName: "Live", PasswordHash: "x"})

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipLoginTaken {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipLoginTaken)
	}
}

func TestBulkSetStatusRecordsTheRightAuditActionForEachMove(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want string
	}{
		{"to blocked", users.StatusActive, users.StatusBlocked, audit.ActionUserBlock},
		{"to deleted", users.StatusActive, users.StatusDeleted, audit.ActionUserDelete},
		{"deleted back to active", users.StatusDeleted, users.StatusActive, audit.ActionUserRestore},
		{"blocked back to active", users.StatusBlocked, users.StatusActive, audit.ActionUserUnblock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newBulkFixture(t)
			u := f.createUser(t, "mover")
			if tc.from != users.StatusActive {
				if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
					[]uuid.UUID{u.ID}, tc.from, "setup"); err != nil {
					t.Fatalf("BulkSetStatus() setup = %v", err)
				}
			}

			if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
				[]uuid.UUID{u.ID}, tc.to, "reason"); err != nil {
				t.Fatalf("BulkSetStatus() = %v", err)
			}

			actions := f.sink.actions()
			if len(actions) == 0 || actions[len(actions)-1] != tc.want {
				t.Errorf("last recorded audit action = %v, want %q", actions, tc.want)
			}
		})
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

// Only this test exercises the u.Email != "" guard; in the shared-login test
// the login arm fires first.
func TestBulkBlockSkipsASecondDeletedAccountWithEmptyEmailField(t *testing.T) {
	f := newBulkFixture(t)
	first := f.repo.Add(users.User{Login: "first", FullName: "First", PasswordHash: "x"})
	second := f.repo.Add(users.User{Login: "second", FullName: "Second", PasswordHash: "x"})
	other := f.createUser(t, "unrelated")

	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID, other.ID}, users.StatusBlocked, "sweep")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	changed := map[uuid.UUID]bool{}
	for _, id := range res.Changed {
		changed[id] = true
	}
	if !changed[first.ID] || !changed[second.ID] || !changed[other.ID] {
		t.Fatalf("Changed = %v, want all three accounts: both deleted accounts with empty email must not collide with each other", res.Changed)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}
}

func TestBulkBlockWithANewReasonUpdatesAnAlreadyBlockedAccount(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	other := f.createUser(t, "petrov")
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID}, users.StatusBlocked, "suspected cheating"); err != nil {
		t.Fatalf("BulkSetStatus() setup = %v", err)
	}

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID, other.ID}, users.StatusBlocked, "confirmed cheating in the October contest")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	changed := map[uuid.UUID]bool{}
	for _, id := range res.Changed {
		changed[id] = true
	}
	if !changed[target.ID] || !changed[other.ID] {
		t.Fatalf("Changed = %v, want both accounts: an already-blocked account with a new reason must be accepted", res.Changed)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}
	after, ok := f.repo.Get(target.ID)
	if !ok || after.StatusReason != "confirmed cheating in the October contest" {
		t.Errorf("StatusReason = %q, want the corrected reason", after.StatusReason)
	}
}

func TestBulkBlockSkipsAnAlreadyBlockedAccountWithTheIdenticalReason(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID}, users.StatusBlocked, "suspected cheating"); err != nil {
		t.Fatalf("BulkSetStatus() setup = %v", err)
	}

	res, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{target.ID}, users.StatusBlocked, "suspected cheating")
	if err != nil {
		t.Fatalf("BulkSetStatus() returned error: %v", err)
	}

	if len(res.Changed) != 0 {
		t.Errorf("Changed = %v, want none: the reason did not change", res.Changed)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipAlreadyInStatus {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipAlreadyInStatus)
	}
}

func TestBulkSetStatusRefusesEmptySelection(t *testing.T) {
	f := newBulkFixture(t)

	_, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{}, users.StatusBlocked, "why")

	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("err = %v, want ErrInvalidAccount", err)
	}
}

func TestBulkRolesKeepsOneAdministrator(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createAdmin(t, "admin-one")
	second := f.createAdmin(t, "admin-two")

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, []string{"student"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 1 {
		t.Fatalf("Changed = %v, want exactly one account", res.Changed)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipLastAdministrator {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipLastAdministrator)
	}
}

func TestBulkReplaceRolesCountsAdministratorsOnceForTheWholeSelection(t *testing.T) {
	f := newBulkFixture(t)
	ids := make([]uuid.UUID, 0, 5)
	for i := 0; i < 5; i++ {
		admin := f.createAdmin(t, fmt.Sprintf("admin-%d", i))
		ids = append(ids, admin.ID)
	}

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID, ids, []string{"student"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 4 {
		t.Fatalf("Changed = %v, want 4 accounts demoted and one kept as the last administrator", res.Changed)
	}
	if f.repo.CountActiveWithRoleCalls != 1 {
		t.Errorf("CountActiveWithRoleCalls = %d, want 1: the count must be taken once for the whole selection", f.repo.CountActiveWithRoleCalls)
	}
}

func TestBulkReplaceRolesNeverSpendsTheBudgetWhenTheNewRolesKeepAdmin(t *testing.T) {
	f := newBulkFixture(t)
	sole := f.createAdmin(t, "sole-admin")

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID,
		[]uuid.UUID{sole.ID}, []string{users.RoleAdmin, "instructor"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 1 || res.Changed[0] != sole.ID {
		t.Fatalf("Changed = %v, want [%v]: keeping the admin role must not be treated as a demotion", res.Changed, sole.ID)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}
	if f.repo.CountActiveWithRoleCalls != 0 {
		t.Errorf("CountActiveWithRoleCalls = %d, want 0: a role set that keeps admin never puts the budget at risk", f.repo.CountActiveWithRoleCalls)
	}
}

// Block itself spends the budget once in setup, so calls are counted from
// after it.
func TestBulkReplaceRolesNeverSpendsTheBudgetForABlockedAdministrator(t *testing.T) {
	f := newBulkFixture(t)
	f.createAdmin(t, "admin-one")
	second := f.createAdmin(t, "admin-two")
	if err := f.service.Block(context.Background(), f.admin.ID, second.ID, "on leave"); err != nil {
		t.Fatalf("Block() returned error: %v", err)
	}
	before := f.repo.CountActiveWithRoleCalls

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID,
		[]uuid.UUID{second.ID}, []string{"student"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 1 || res.Changed[0] != second.ID {
		t.Fatalf("Changed = %v, want [%v]: a blocked administrator cannot administer, so demoting it is free", res.Changed, second.ID)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}
	if f.repo.CountActiveWithRoleCalls != before {
		t.Errorf("CountActiveWithRoleCalls = %d, want %d: an inactive account never spends the budget", f.repo.CountActiveWithRoleCalls, before)
	}
}

func TestBulkRolesSkipsDeletedAccounts(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, gone.ID, "graduated"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, []string{"student"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("Changed = %v, want none", res.Changed)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipDeleted {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipDeleted)
	}
}

func TestBulkReplaceRolesAppliesInOneTransaction(t *testing.T) {
	f := newBulkFixture(t)
	ids := []uuid.UUID{
		f.createUser(t, "a").ID,
		f.createUser(t, "b").ID,
		f.createUser(t, "c").ID,
	}

	if _, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID, ids, []string{"student"}); err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if f.uow.Calls != 1 {
		t.Errorf("uow.Calls = %d, want 1", f.uow.Calls)
	}
}

// Deleting in setup opens a transaction, so the count is compared against its
// value just before the call.
func TestBulkReplaceRolesOpensNoTransactionWhenNothingSurvives(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, gone.ID, "graduated"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	before := f.uow.Calls

	res, err := f.service.BulkReplaceRoles(context.Background(), f.admin.ID, []uuid.UUID{gone.ID}, []string{"student"})
	if err != nil {
		t.Fatalf("BulkReplaceRoles() returned error: %v", err)
	}
	if len(res.Changed) != 0 {
		t.Errorf("Changed = %v, want none", res.Changed)
	}
	if f.uow.Calls != before {
		t.Errorf("uow.Calls = %d, want %d: nothing survived classify, so no transaction should have opened", f.uow.Calls, before)
	}
}

func TestBulkResetPasswordIssuesOnePerAccount(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createUser(t, "ivanov")
	second := f.createUser(t, "petrov")

	res, err := f.service.BulkResetPassword(context.Background(), f.admin.ID, []uuid.UUID{first.ID, second.ID})
	if err != nil {
		t.Fatalf("BulkResetPassword() returned error: %v", err)
	}
	if len(res.Issued) != 2 {
		t.Fatalf("Issued = %v, want 2 accounts", res.Issued)
	}
	if res.Issued[0].OneTimePassword == res.Issued[1].OneTimePassword {
		t.Errorf("both accounts got the same password: %q", res.Issued[0].OneTimePassword)
	}

	after, err := f.repo.ByID(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("ByID() returned error: %v", err)
	}
	if !after.MustChangePassword {
		t.Errorf("MustChangePassword = false, want true")
	}
	if after.SessionGeneration <= first.SessionGeneration {
		t.Errorf("SessionGeneration = %d, want greater than %d", after.SessionGeneration, first.SessionGeneration)
	}
}

// Also checks that an emptied selection opens no transaction; deleting in
// setup opens one, so the count is compared against its value just before.
func TestBulkResetPasswordSkipsDeletedAccounts(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, gone.ID, "graduated"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	before := f.uow.Calls

	res, err := f.service.BulkResetPassword(context.Background(), f.admin.ID, []uuid.UUID{gone.ID})
	if err != nil {
		t.Fatalf("BulkResetPassword() returned error: %v", err)
	}
	if len(res.Issued) != 0 {
		t.Errorf("Issued = %v, want none", res.Issued)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipDeleted {
		t.Fatalf("Skipped = %+v, want one entry with reason %q", res.Skipped, users.SkipDeleted)
	}
	if f.uow.Calls != before {
		t.Errorf("uow.Calls = %d, want %d: nothing survived classify, so no transaction should have opened", f.uow.Calls, before)
	}
}

func TestBulkResetPasswordAppliesInOneTransaction(t *testing.T) {
	f := newBulkFixture(t)
	ids := []uuid.UUID{
		f.createUser(t, "a").ID,
		f.createUser(t, "b").ID,
		f.createUser(t, "c").ID,
	}

	if _, err := f.service.BulkResetPassword(context.Background(), f.admin.ID, ids); err != nil {
		t.Fatalf("BulkResetPassword() returned error: %v", err)
	}
	if f.uow.Calls != 1 {
		t.Errorf("uow.Calls = %d, want 1", f.uow.Calls)
	}
}

// CLAUDE.md rule 8. A broken crypto/rand crashes the process
// (https://go.dev/issue/66821), so a cancelled context drives the same
// group.Wait error path instead.
func TestBulkResetPasswordAbortsWhenTheParallelPhaseFailsRatherThanSkipping(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createUser(t, "ivanov")
	second := f.createUser(t, "petrov")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := f.service.BulkResetPassword(ctx, f.admin.ID, []uuid.UUID{first.ID, second.ID})
	if err == nil {
		t.Fatalf("BulkResetPassword() = %+v, %v, want an error", res, err)
	}
	if len(res.Issued) != 0 || len(res.Skipped) != 0 {
		t.Errorf("BulkResetPassword() returned %+v on failure, want a zero result", res)
	}
	if f.uow.Calls != 0 {
		t.Errorf("uow.Calls = %d, want 0: a failure before the transaction must never open one", f.uow.Calls)
	}

	after, getErr := f.repo.ByID(context.Background(), first.ID)
	if getErr != nil {
		t.Fatalf("ByID() returned error: %v", getErr)
	}
	if after.MustChangePassword {
		t.Errorf("the account was updated despite the phase failing")
	}
}

func TestBulkResetPasswordWaitsLongerThanASignInForAHashingSlot(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createUser(t, "ivanov")
	service, _, release := busyService(t, f.repo)
	time.AfterFunc(200*time.Millisecond, release)

	res, err := service.BulkResetPassword(context.Background(), f.admin.ID, []uuid.UUID{first.ID})

	if err != nil {
		t.Fatalf("BulkResetPassword() = %v, want it to wait for the slot", err)
	}
	if len(res.Issued) != 1 {
		t.Errorf("issued %d passwords, want 1", len(res.Issued))
	}
}
