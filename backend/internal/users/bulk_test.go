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
	// The property the two-phase design exists to protect: the budget is
	// still spent from a single count of the whole selection, not one count
	// per candidate, even now that fetching it is lazy.
	if f.repo.CountActiveWithRoleCalls != 1 {
		t.Errorf("CountActiveWithRoleCalls = %d, want 1", f.repo.CountActiveWithRoleCalls)
	}
}

// TestBulkStatusNeverAsksForTheAdministratorBudgetWhenNothingCanSpendIt
// guards the laziness itself: a selection that turns out to hold no active
// administrator must never cost the count query at all, which is only true
// if fetching it happens on first spend rather than up front.
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

// TestBulkBlockSkipsADeletedAccountWhoseLoginWasReclaimed guards the defect
// where TakenAmong was consulted only when the destination was active. The
// partial unique indexes are WHERE status <> 'deleted', so moving a deleted
// account to blocked re-imposes uniqueness on its login exactly as restoring
// it to active does: a deleted account whose login a live account has since
// reclaimed must be skipped, not let through to trip the constraint and
// abort accounts around it that would otherwise have gone through.
func TestBulkBlockSkipsADeletedAccountWhoseLoginWasReclaimed(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.createUser(t, "reclaimed")
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	// A live account has since taken the login the deleted one released.
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

// TestBulkBlockReportsAnEmailCollisionAsEmailTaken guards against TakenAmong
// collapsing a login match and an email match into one undifferentiated
// list: an account whose only conflict is its email must be reported with
// SkipEmailTaken, not SkipLoginTaken, so the administrator looks at the
// right field.
func TestBulkBlockReportsAnEmailCollisionAsEmailTaken(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.repo.Add(users.User{
		Login: "gone-by-email", Email: "shared@example.com", FullName: "Gone", PasswordHash: "x",
	})
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	// A live account holds a different login but the same email.
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

// TestBulkBlockSkipsASecondDeletedAccountSharingAReclaimedLogin guards the
// case TakenAmong cannot see: it only compares a deleted account against a
// LIVE one, but the partial unique indexes (WHERE status <> 'deleted') let
// many deleted rows share a login freely — exactly what "delete ivanov,
// create ivanov, delete again" produces. The moment both leave "deleted" in
// the same selection, the second one to apply collides with the first, and
// that must skip the second account rather than trip the constraint and
// abort the whole batch.
func TestBulkBlockSkipsASecondDeletedAccountSharingAReclaimedLogin(t *testing.T) {
	f := newBulkFixture(t)
	first := f.createUser(t, "ivanov")
	// A second account that only exists because "ivanov" was deleted and
	// then recreated; the in-memory repository's Add, unlike Create, does not
	// enforce login uniqueness, so this mirrors that history directly.
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

// TestBulkBlockReportsLoginWhenBothLoginAndEmailCollide guards the
// login-over-email precedence: when a deleted account's move would collide
// on both fields at once, the login is reported because it is what an
// administrator searches by. Every other test in this file sets exactly one
// of the two flags, so this is the only one that would catch the switch
// arms in BulkSetStatus's TakenAmong fold being swapped.
func TestBulkBlockReportsLoginWhenBothLoginAndEmailCollide(t *testing.T) {
	f := newBulkFixture(t)
	gone := f.repo.Add(users.User{
		Login: "collide", Email: "shared@example.com", FullName: "Gone", PasswordHash: "x",
	})
	if _, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{gone.ID}, users.StatusDeleted, "left"); err != nil {
		t.Fatalf("BulkSetStatus() deleting = %v", err)
	}
	// A live account holding both the same login and the same email at once.
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

// TestBulkSetStatusRecordsTheRightAuditActionForEachMove exercises
// statusAction end to end: coming back to active is two different events
// depending on where the account came from, and only a test that inspects
// the recorded action rather than just the returned status catches
// statusAction naming the wrong one.
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

// TestBulkBlockSkipsASecondDeletedAccountWithEmptyEmailField guards the guard
// that prevents two deleted accounts with no email from colliding with each
// other when both leave the deleted status in the same selection. The email
// arm of the reclaim fold is guarded by u.Email != "" to avoid treating two
// accounts with no email as if they share a value, which only this test
// exercises: the existing shared-login test would still pass without this
// guard because the login arm fires first.
func TestBulkBlockSkipsASecondDeletedAccountWithEmptyEmailField(t *testing.T) {
	f := newBulkFixture(t)
	// Two deleted accounts with different logins and no email at all.
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

// TestBulkBlockWithANewReasonUpdatesAnAlreadyBlockedAccount guards the same
// rule as the single-account tests in service_test.go, on the bulk surface:
// one machine decides both, so a selection naming an account already in the
// target status must still accept it — and record the second decision — when
// the reason differs. This is the intended fan-out of the fix: an
// administrator who names an already-blocked account in a bulk block, giving
// a reason for the whole selection, expects that reason to land on it too.
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

// TestBulkBlockSkipsAnAlreadyBlockedAccountWithTheIdenticalReason is the
// no-op half on the bulk surface: nothing would change, so the account is
// still reported as skipped rather than needlessly rewritten and re-audited.
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

// TestBulkSetStatusRefusesEmptySelection guards the branch in boundSelection
// that refuses a selection with no accounts. An empty slice should return an
// error wrapped in ErrInvalidAccount rather than proceeding to apply zero
// accounts, which would succeed trivially and miss the guard's purpose.
func TestBulkSetStatusRefusesEmptySelection(t *testing.T) {
	f := newBulkFixture(t)

	_, err := f.service.BulkSetStatus(context.Background(), f.admin.ID,
		[]uuid.UUID{}, users.StatusBlocked, "why")

	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("err = %v, want ErrInvalidAccount", err)
	}
}

// TestBulkRolesKeepsOneAdministrator guards the same defect the status
// budget guards against, on the role surface: two administrators demoted
// together must not both go through, each seeing the other still standing.
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

// TestBulkReplaceRolesCountsAdministratorsOnceForTheWholeSelection guards the
// point of adminBudget's laziness and its cache: a selection of several
// administrators being demoted at once must cost exactly one
// CountActiveWithRole, not one per candidate — a per-account count would let
// every one of them see the others still standing and all pass.
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

// TestBulkReplaceRolesNeverSpendsTheBudgetWhenTheNewRolesKeepAdmin guards the
// first half of the mirror with the single-account ReplaceRoles: the budget
// is only at risk when the new role set drops the administrator role. The
// sole administrator here is given a role set that still includes admin, so
// nothing is actually being taken away, and the operation must succeed
// without ever consulting how many administrators remain.
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

// TestBulkReplaceRolesNeverSpendsTheBudgetForABlockedAdministrator guards the
// second half of the mirror: only an account that can administer today —
// meaning it is active — is one the budget protects. The first administrator
// stays active and the second is blocked; demoting the second must go
// through without a further count query, exactly as refuseIfLastAdmin does
// not decrement for an inactive account. Block itself spends the budget once
// (it is built on BulkSetStatus, which protects an active administrator being
// blocked), so the calls are counted from right after that setup rather than
// from zero.
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

// TestBulkRolesSkipsDeletedAccounts guards SkipDeleted's reason for being:
// giving roles to an account nobody can sign into is pointless, so a deleted
// account must be skipped rather than touched.
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

// TestBulkReplaceRolesAppliesInOneTransaction guards the point of the two
// phases on the role surface, exactly as TestBulkStatusAppliesInOneTransaction
// does for status: deciding costs no transaction, and applying costs exactly
// one however many accounts survived.
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

// TestBulkReplaceRolesOpensNoTransactionWhenNothingSurvives mirrors
// TestBulkStatusOpensNoTransactionWhenNothingSurvives on the role surface: a
// selection that classify empties out entirely — here, an account skipped as
// deleted — must never open the transaction at all. Deleting the account in
// setup already opens one transaction of its own, so the count is compared
// against its value right before the call under test rather than zero.
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

// TestBulkResetPasswordIssuesOnePerAccount guards the point of the whole
// operation: every account gets its own password, never a group's shared
// one, and the account is left exactly as a single ResetPassword leaves it —
// due for a change and with its old sessions retired.
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
	// Two accounts, two different passwords: one password for a group would be
	// one password to share.
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

// TestBulkResetPasswordSkipsDeletedAccounts mirrors
// TestBulkRolesSkipsDeletedAccounts on the password surface: a one-time
// password for an account nobody can sign into is pointless. It also guards
// TestBulkStatusOpensNoTransactionWhenNothingSurvives's property on this
// surface: a selection that classify empties out entirely — the only account
// named here is skipped — must never open the transaction, even though
// hashing has its own worker pool ahead of it. Deleting the account in setup
// already opens one transaction of its own, so the count is compared against
// its value right before the call under test rather than zero.
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

// TestBulkResetPasswordAppliesInOneTransaction guards the point of the two
// phases on the password surface: applying costs exactly one transaction
// however many accounts survived, even though hashing itself runs in
// parallel workers ahead of it.
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

// TestBulkResetPasswordAbortsWhenTheParallelPhaseFailsRatherThanSkipping
// guards CLAUDE.md rule 8: only a row-level reason from the closed skip
// vocabulary may turn into a skipped entry. A failure in the parallel
// hashing phase — the pool of workers each running generatePassword and
// password.Hash ahead of the transaction — is the database or the machine
// itself failing, not anything about a particular account, so it must abort
// the whole operation and surface as an error, never be reported as accounts
// somebody has to go and fix.
//
// The natural way to force that phase to fail is to break crypto/rand, but
// on this Go toolchain (see https://go.dev/issue/66821) a broken
// crypto/rand.Reader makes Read crash the process outright rather than
// return an error, so it cannot be used as a test fixture. An already
// canceled context takes the identical code path instead: each worker checks
// groupCtx.Err() before it ever calls generatePassword or password.Hash, so
// this exercises exactly the group.Go/group.Wait error plumbing a genuine
// hashing failure would use, just entered a different way.
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
	// The same reasoning as an import: the batch is an administrator's, and
	// refusing it because anonymous sign-ins hold the slots for a moment would
	// make a routine operation fail under exactly the load it is run during.
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
