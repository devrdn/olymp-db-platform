package users_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// collectingSink keeps audit entries for assertions.
type collectingSink struct {
	entries []audit.Entry
	err     error
}

func (s *collectingSink) Append(_ context.Context, e audit.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

func (s *collectingSink) AppendMany(_ context.Context, entries []audit.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, entries...)
	return nil
}

func (s *collectingSink) actions() []string {
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e.Action)
	}
	return out
}

type fixture struct {
	service *users.Service
	repo    *userstest.Repository
	sink    *collectingSink
	uow     *userstest.SpyUnitOfWork
	actor   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := userstest.New()
	sink := &collectingSink{}
	uow := &userstest.SpyUnitOfWork{}
	return &fixture{
		service: users.NewService(repo, audit.New(sink), uow, passwordtest.NewHasher()),
		repo:    repo,
		sink:    sink,
		uow:     uow,
		actor:   uuid.New(),
	}
}

// addUser stores an account with a known password.
func (f *fixture) addUser(t *testing.T, login, plaintext string) users.User {
	t.Helper()
	return f.repo.Add(users.User{Login: login, FullName: "Test User", PasswordHash: passwordtest.Hash(t, plaintext)})
}

func TestCreateStoresTheAccount(t *testing.T) {
	f := newFixture(t)

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID:  f.actor,
		Login:    "petrov",
		FullName: "Pyotr Petrov",
		Roles:    []string{"student"},
	})

	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	if created.User.Login != "petrov" {
		t.Errorf("Login = %q, want petrov", created.User.Login)
	}
	if _, ok := f.repo.Get(created.User.ID); !ok {
		t.Error("the account was not stored")
	}
}

func TestCreateIssuesAOneTimePassword(t *testing.T) {
	// The administrator never chooses somebody else's lasting password.
	f := newFixture(t)

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "petrov", FullName: "Pyotr Petrov",
	})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	if created.OneTimePassword == "" {
		t.Fatal("Create() returned no one-time password to hand over")
	}
	stored, _ := f.repo.Get(created.User.ID)
	if !stored.MustChangePassword {
		t.Error("the account was not marked as needing a password change")
	}

	if !passwordtest.Matches(t, stored.PasswordHash, created.OneTimePassword) {
		t.Error("the issued password does not match the stored digest")
	}
}

func TestCreateNeverStoresThePasswordInClear(t *testing.T) {
	f := newFixture(t)

	created, _ := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "petrov", FullName: "Pyotr Petrov",
	})

	stored, _ := f.repo.Get(created.User.ID)
	if strings.Contains(stored.PasswordHash, created.OneTimePassword) {
		t.Error("the stored digest contains the plaintext password")
	}
}

func TestCreateRejectsADuplicateLogin(t *testing.T) {
	f := newFixture(t)
	f.addUser(t, "petrov", "some password")

	_, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "PETROV", FullName: "Another Petrov",
	})

	if !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("err = %v, want ErrLoginTaken for a login differing only in case", err)
	}
}

// TestCreateAllowsRecreatingADeletedLogin is the recovery workflow deletion
// exists for: a deleted account's login must not go on blocking the
// duplicate-login pre-check, or an account deleted by mistake could never be
// created again under the same login. ByLogin still resolves to the deleted
// account here (nothing live has reclaimed the login), so what is under test
// is that Create's pre-check looks at its Status rather than treating any
// match as taken.
func TestCreateAllowsRecreatingADeletedLogin(t *testing.T) {
	f := newFixture(t)
	gone := f.addUser(t, "petrov", "old password")
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{gone.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "created by mistake", By: f.actor}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "petrov", FullName: "New Petrov",
	})

	if err != nil {
		t.Fatalf("Create() after delete = %v, want the login to be free again", err)
	}
	if created.User.ID == gone.ID {
		t.Error("Create() returned the deleted account instead of a new one")
	}
}

func TestCreateRejectsAnEmptyLogin(t *testing.T) {
	f := newFixture(t)

	_, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "  ", FullName: "Nameless",
	})

	if err == nil {
		t.Error("Create() accepted a blank login")
	}
}

func TestCreateIsAudited(t *testing.T) {
	f := newFixture(t)

	_, _ = f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "petrov", FullName: "Pyotr Petrov",
	})

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserCreate {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserCreate)
	}
}

func TestAuditOfCreationCarriesNoPassword(t *testing.T) {
	f := newFixture(t)

	created, _ := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "petrov", FullName: "Pyotr Petrov",
	})

	for _, entry := range f.sink.entries {
		for key, value := range entry.Payload {
			if str, ok := value.(string); ok && str == created.OneTimePassword {
				t.Errorf("audit payload field %q carries the one-time password", key)
			}
		}
	}
}

func TestBlockRetiresEverySessionOfTheAccount(t *testing.T) {
	// Blocking that only prevents the next login would leave a disqualified
	// participant working in the tab they already have open.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	before, _ := f.repo.Get(user.ID)

	if err := f.service.Block(context.Background(), f.actor, user.ID, "cheating in the October contest"); err != nil {
		t.Fatalf("Block() returned error: %v", err)
	}

	after, _ := f.repo.Get(user.ID)
	if after.Status != users.StatusBlocked {
		t.Errorf("Status = %q, want blocked", after.Status)
	}
	if after.SessionGeneration <= before.SessionGeneration {
		t.Error("the session generation was not advanced, so open sessions survive")
	}
}

func TestBlockIsAudited(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")

	_ = f.service.Block(context.Background(), f.actor, user.ID, "cheating in the October contest")

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserBlock {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserBlock)
	}
}

func TestAdministratorsCannotBlockThemselves(t *testing.T) {
	// Locking the last administrator out of the installation is an easy
	// mistake and an expensive one to undo.
	f := newFixture(t)
	user := f.addUser(t, "admin", "some password")

	err := f.service.Block(context.Background(), user.ID, user.ID, "some reason")

	if !errors.Is(err, users.ErrCannotActOnSelf) {
		t.Errorf("err = %v, want ErrCannotActOnSelf", err)
	}
}

func TestUnblockRestoresAccess(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	_ = f.service.Block(context.Background(), f.actor, user.ID, "cheating in the October contest")

	if err := f.service.Unblock(context.Background(), f.actor, user.ID); err != nil {
		t.Fatalf("Unblock() returned error: %v", err)
	}

	after, _ := f.repo.Get(user.ID)
	if after.Status != users.StatusActive {
		t.Errorf("Status = %q, want active", after.Status)
	}
}

func TestBlockRequiresAReason(t *testing.T) {
	// Blocking is a thing an administrator is answered to for later, and "no
	// reason given" is not an answer the trail can carry.
	f := newFixture(t)
	target := f.addUser(t, "ivanov", "some password")

	err := f.service.Block(context.Background(), f.actor, target.ID, "   ")

	if !errors.Is(err, users.ErrReasonRequired) {
		t.Errorf("err = %v, want ErrReasonRequired", err)
	}

	// And the account was not touched on the way to refusing.
	after, _ := f.repo.Get(target.ID)
	if after.Status != users.StatusActive {
		t.Errorf("Status = %q, want active; a refused block must not touch the account", after.Status)
	}
}

func TestBlockBoundsTheReason(t *testing.T) {
	f := newFixture(t)
	target := f.addUser(t, "ivanov", "some password")

	err := f.service.Block(context.Background(), f.actor, target.ID, strings.Repeat("x", users.MaxStatusReasonLength+1))

	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("err = %v, want ErrInvalidAccount", err)
	}
}

func TestBlockKeepsTheReason(t *testing.T) {
	f := newFixture(t)
	target := f.addUser(t, "ivanov", "some password")

	if err := f.service.Block(context.Background(), f.actor, target.ID, "cheating in the October contest"); err != nil {
		t.Fatalf("Block() returned error: %v", err)
	}

	after, _ := f.repo.Get(target.ID)
	if after.StatusReason != "cheating in the October contest" {
		t.Errorf("StatusReason = %q, want %q", after.StatusReason, "cheating in the October contest")
	}
	if after.StatusChangedBy == nil || *after.StatusChangedBy != f.actor {
		t.Errorf("StatusChangedBy = %v, want %v", after.StatusChangedBy, f.actor)
	}
}

// TestReblockingWithANewReasonUpdatesTheStoredReason guards the regression
// the old unconditional-skip behaviour would reintroduce: an administrator
// who blocks an account, then blocks it again with a stronger reason, must
// see that second decision land. The old Block body wrote unconditionally;
// routing through BulkSetStatus's "already in status" skip must not silently
// discard it.
func TestReblockingWithANewReasonUpdatesTheStoredReason(t *testing.T) {
	f := newFixture(t)
	target := f.addUser(t, "ivanov", "some password")
	if err := f.service.Block(context.Background(), f.actor, target.ID, "suspected cheating"); err != nil {
		t.Fatalf("first Block() returned error: %v", err)
	}
	f.sink.entries = nil // isolate what the second block records

	if err := f.service.Block(context.Background(), f.actor, target.ID,
		"confirmed cheating in the October contest"); err != nil {
		t.Fatalf("second Block() returned error: %v", err)
	}

	after, _ := f.repo.Get(target.ID)
	if after.StatusReason != "confirmed cheating in the October contest" {
		t.Errorf("StatusReason = %q, want the corrected reason", after.StatusReason)
	}
	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserBlock {
		t.Errorf("audit actions = %v, want one %q recording the second decision", got, audit.ActionUserBlock)
	}
}

// TestReblockingWithTheIdenticalReasonIsANoOp is the other half of the same
// rule: when nothing would actually change — same status, same reason — the
// account is skipped, the stored reason is untouched, and nothing new is
// audited.
func TestReblockingWithTheIdenticalReasonIsANoOp(t *testing.T) {
	f := newFixture(t)
	target := f.addUser(t, "ivanov", "some password")
	if err := f.service.Block(context.Background(), f.actor, target.ID, "suspected cheating"); err != nil {
		t.Fatalf("first Block() returned error: %v", err)
	}
	before, _ := f.repo.Get(target.ID)
	f.sink.entries = nil
	callsBefore := f.uow.Calls

	if err := f.service.Block(context.Background(), f.actor, target.ID, "suspected cheating"); err != nil {
		t.Fatalf("second Block() returned error: %v", err)
	}

	after, _ := f.repo.Get(target.ID)
	if after.StatusReason != before.StatusReason {
		t.Errorf("StatusReason = %q, want it unchanged at %q", after.StatusReason, before.StatusReason)
	}
	if len(f.sink.entries) != 0 {
		t.Errorf("audit entries = %v, want none recorded for a no-op", f.sink.entries)
	}
	if f.uow.Calls != callsBefore {
		t.Errorf("uow.Calls = %d, want %d: a no-op must not open a transaction", f.uow.Calls, callsBefore)
	}
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	// Otherwise anyone who borrows an unlocked browser takes the account over.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")

	err := f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID:      user.ID,
		OldPassword: "not the old password",
		NewPassword: "a brand new password",
	})

	if !errors.Is(err, users.ErrWrongPassword) {
		t.Errorf("err = %v, want ErrWrongPassword", err)
	}
}

func TestChangePasswordReplacesTheDigest(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")

	err := f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID:      user.ID,
		OldPassword: "old password",
		NewPassword: "a brand new password",
	})
	if err != nil {
		t.Fatalf("ChangePassword() returned error: %v", err)
	}

	after, _ := f.repo.Get(user.ID)
	if !passwordtest.Matches(t, after.PasswordHash, "a brand new password") {
		t.Error("the new password does not verify against the stored digest")
	}
	if passwordtest.Matches(t, after.PasswordHash, "old password") {
		t.Error("the old password still works")
	}
}

func TestChangePasswordRetiresOtherSessions(t *testing.T) {
	// Changing a password is what someone does when they suspect their account
	// is in use elsewhere; it has to end those sessions.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")
	before, _ := f.repo.Get(user.ID)

	_ = f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID: user.ID, OldPassword: "old password", NewPassword: "a brand new password",
	})

	after, _ := f.repo.Get(user.ID)
	if after.SessionGeneration <= before.SessionGeneration {
		t.Error("the session generation was not advanced, so other sessions survive")
	}
}

func TestChangePasswordClearsTheOneTimeFlag(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")
	_ = f.repo.SetPassword(context.Background(), user.ID, mustHash(t, "old password"), true)

	_ = f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID: user.ID, OldPassword: "old password", NewPassword: "a brand new password",
	})

	after, _ := f.repo.Get(user.ID)
	if after.MustChangePassword {
		t.Error("the account is still marked as needing a password change")
	}
}

func TestChangePasswordRejectsAShortPassword(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")

	err := f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID: user.ID, OldPassword: "old password", NewPassword: "short",
	})

	if !errors.Is(err, users.ErrWeakPassword) {
		t.Errorf("err = %v, want ErrWeakPassword", err)
	}
}

func TestChangePasswordRejectsReusingTheCurrentOne(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password long enough")

	err := f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID:      user.ID,
		OldPassword: "old password long enough",
		NewPassword: "old password long enough",
	})

	if !errors.Is(err, users.ErrSamePassword) {
		t.Errorf("err = %v, want ErrSamePassword", err)
	}
}

func TestResetPasswordIssuesANewOneTimePassword(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "forgotten password")

	issued, err := f.service.ResetPassword(context.Background(), f.actor, user.ID)
	if err != nil {
		t.Fatalf("ResetPassword() returned error: %v", err)
	}

	after, _ := f.repo.Get(user.ID)
	if !passwordtest.Matches(t, after.PasswordHash, issued) {
		t.Error("the issued password does not verify against the stored digest")
	}
	if !after.MustChangePassword {
		t.Error("the reset account was not marked as needing a password change")
	}
	if after.SessionGeneration <= user.SessionGeneration {
		t.Error("a password reset did not retire existing sessions")
	}
}

func TestResetPasswordIsAudited(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "forgotten password")

	_, _ = f.service.ResetPassword(context.Background(), f.actor, user.ID)

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserPasswordReset {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserPasswordReset)
	}
}

func TestReplaceRolesRecordsBothTheOldAndNewSet(t *testing.T) {
	// A privilege change is exactly what an investigation asks about later, so
	// the entry has to show what it changed from.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	_ = f.repo.ReplaceRoles(context.Background(), user.ID, []string{"student"})

	if err := f.service.ReplaceRoles(context.Background(), f.actor, user.ID, []string{"organizer"}); err != nil {
		t.Fatalf("ReplaceRoles() returned error: %v", err)
	}

	entry := f.sink.entries[0]
	if entry.Action != audit.ActionUserRolesChange {
		t.Fatalf("action = %q, want %q", entry.Action, audit.ActionUserRolesChange)
	}
	// Under the one shape every change now takes, so the panel can render a
	// change without knowing which action produced it.
	roles, ok := entry.Payload["changes"].(map[string]any)["roles"].(map[string]any)
	if !ok || roles["from"] == nil || roles["to"] == nil {
		t.Errorf("payload = %v, want both the previous and the new roles", entry.Payload)
	}
}

func TestReplaceRolesRetiresSessionsSoNewLimitsApplyAtOnce(t *testing.T) {
	// A demotion that only takes effect at the next login would leave someone
	// exercising rights they no longer hold.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	before, _ := f.repo.Get(user.ID)

	_ = f.service.ReplaceRoles(context.Background(), f.actor, user.ID, []string{"student"})

	after, _ := f.repo.Get(user.ID)
	if after.SessionGeneration <= before.SessionGeneration {
		t.Error("the session generation was not advanced after a role change")
	}
}

func TestOperationsOnAMissingAccountReportNotFound(t *testing.T) {
	f := newFixture(t)
	missing := uuid.New()
	ctx := context.Background()

	if err := f.service.Block(ctx, f.actor, missing, "some reason"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("Block() = %v, want ErrNotFound", err)
	}
	if _, err := f.service.ResetPassword(ctx, f.actor, missing); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ResetPassword() = %v, want ErrNotFound", err)
	}
}

// TestOperationsOnADeletedAccountReportAccountDeleted closes the gap the bulk
// path already closed: BulkReplaceRoles and BulkResetPassword both skip a
// deleted account in a selection (SkipDeleted), but until now the
// single-account UpdateProfile, ResetPassword and ReplaceRoles had no such
// guard at all — an administrator could reset a deleted account's password
// through this endpoint and be refused through the bulk one for the identical
// operation.
func TestOperationsOnADeletedAccountReportAccountDeleted(t *testing.T) {
	f := newFixture(t)
	deleted := f.repo.Add(users.User{
		Login: "gone", FullName: "Gone Petrov", Status: users.StatusDeleted,
	})
	ctx := context.Background()

	if err := f.service.UpdateProfile(ctx, f.actor, deleted.ID, "New Name", ""); !errors.Is(err, users.ErrAccountDeleted) {
		t.Errorf("UpdateProfile() = %v, want ErrAccountDeleted", err)
	}
	if _, err := f.service.ResetPassword(ctx, f.actor, deleted.ID); !errors.Is(err, users.ErrAccountDeleted) {
		t.Errorf("ResetPassword() = %v, want ErrAccountDeleted", err)
	}
	if err := f.service.ReplaceRoles(ctx, f.actor, deleted.ID, []string{"student"}); !errors.Is(err, users.ErrAccountDeleted) {
		t.Errorf("ReplaceRoles() = %v, want ErrAccountDeleted", err)
	}

	// None of the refusals above may have changed anything.
	stored, _ := f.repo.Get(deleted.ID)
	if stored.FullName != "Gone Petrov" || len(stored.Roles) != 0 {
		t.Errorf("stored = %+v, want the refused operations to leave the account untouched", stored)
	}
}

func mustHash(t *testing.T, plaintext string) string {
	t.Helper()
	return passwordtest.Hash(t, plaintext)
}

func TestBootstrapCreatesTheFirstAdministrator(t *testing.T) {
	// After migrations the installation has no accounts at all, so there has
	// to be a way in that does not itself require being signed in.
	f := newFixture(t)

	result, err := f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")

	if err != nil {
		t.Fatalf("BootstrapAdmin() returned error: %v", err)
	}
	if !result.Created {
		t.Fatal("Created = false on an empty installation")
	}
	if result.OneTimePassword == "" {
		t.Error("no password was issued for the first administrator")
	}

	stored, _ := f.repo.Get(result.User.ID)
	if len(stored.Roles) != 1 || stored.Roles[0] != "admin" {
		t.Errorf("Roles = %v, want [admin]", stored.Roles)
	}
	if !stored.MustChangePassword {
		t.Error("the bootstrap account may keep its generated password")
	}
}

func TestBootstrapIsIdempotent(t *testing.T) {
	// Running it again on a populated installation must not mint a second
	// administrator or reset the first one's password.
	f := newFixture(t)
	first, err := f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")
	if err != nil {
		t.Fatalf("BootstrapAdmin() returned error: %v", err)
	}

	second, err := f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")

	if err != nil {
		t.Fatalf("second BootstrapAdmin() returned error: %v", err)
	}
	if second.Created {
		t.Error("Created = true although the account already exists")
	}
	if second.OneTimePassword != "" {
		t.Error("a password was issued for an account that already exists")
	}

	stored, _ := f.repo.Get(first.User.ID)
	if !passwordtest.Matches(t, stored.PasswordHash, first.OneTimePassword) {
		t.Error("the existing administrator's password was replaced")
	}
}

// TestBootstrapCreatesAFreshAdminWhenTheOldOneWasDeleted is idempotency's
// other half: a deleted administrator under that login is not the reachable
// account BootstrapAdmin is idempotent about, so its login must not be
// treated as already taken care of — the login is free, exactly as Create's
// own duplicate check now treats it.
func TestBootstrapCreatesAFreshAdminWhenTheOldOneWasDeleted(t *testing.T) {
	f := newFixture(t)
	first, err := f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")
	if err != nil {
		t.Fatalf("first BootstrapAdmin() returned error: %v", err)
	}
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{first.User.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "left the university", By: first.User.ID}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	second, err := f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")

	if err != nil {
		t.Fatalf("second BootstrapAdmin() returned error: %v", err)
	}
	if !second.Created {
		t.Error("Created = false, want a fresh administrator: the old one under this login is deleted")
	}
	if second.User.ID == first.User.ID {
		t.Error("BootstrapAdmin() returned the deleted account instead of creating a new one")
	}
	if second.OneTimePassword == "" {
		t.Error("no password was issued for the new administrator")
	}
}

func TestBootstrapIsAudited(t *testing.T) {
	// Creating a privileged account is exactly the kind of event the trail
	// exists for, even when nobody was signed in to do it.
	f := newFixture(t)

	_, _ = f.service.BootstrapAdmin(context.Background(), "root", "Root Administrator")

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserCreate {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserCreate)
	}
	if f.sink.entries[0].ActorID != nil {
		t.Error("the bootstrap entry names an actor; there was none")
	}
}

func TestEveryMultiWriteOperationRunsInsideTheUnitOfWork(t *testing.T) {
	// The transactional seam existed but nothing used it: Block was three
	// independent statements, and an audit failure after SetStatus left a
	// blocked account with no trail. Each operation must run under one Do.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	ctx := context.Background()

	// A slice, not a map: these are not independent. ChangePassword proves
	// knowledge of the current password, and ResetPassword replaces it — so
	// map iteration, whose order Go randomises per run, made this test fail
	// on the runs where the reset happened to go first.
	operations := []struct {
		name string
		run  func() error
	}{
		{"Create", func() error {
			_, err := f.service.Create(ctx, users.CreateCommand{ActorID: f.actor, Login: "new", FullName: "New User"})
			return err
		}},
		{"Block", func() error { return f.service.Block(ctx, f.actor, user.ID, "some reason") }},
		{"Unblock", func() error { return f.service.Unblock(ctx, f.actor, user.ID) }},
		{"UpdateProfile", func() error { return f.service.UpdateProfile(ctx, f.actor, user.ID, "New Name", "") }},
		{"ReplaceRoles", func() error { return f.service.ReplaceRoles(ctx, f.actor, user.ID, []string{"student"}) }},
		{"ChangePassword", func() error {
			return f.service.ChangePassword(ctx, users.ChangePasswordCommand{
				UserID: user.ID, OldPassword: "some password", NewPassword: "a brand new password",
			})
		}},
		{"ResetPassword", func() error { _, err := f.service.ResetPassword(ctx, f.actor, user.ID); return err }},
	}

	for _, operation := range operations {
		name, op := operation.name, operation.run
		before := f.uow.Calls
		if err := op(); err != nil {
			t.Errorf("%s returned error: %v", name, err)
			continue
		}
		if f.uow.Calls != before+1 {
			t.Errorf("%s ran with %d unit-of-work calls, want exactly 1", name, f.uow.Calls-before)
		}
	}
}

func TestAFailedAuditWriteAbortsTheOperation(t *testing.T) {
	// Inside a transaction this becomes a rollback: the action must not
	// survive without its trail.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	f.sink.err = context.DeadlineExceeded

	err := f.service.Block(context.Background(), f.actor, user.ID, "cheating in the October contest")

	if err == nil {
		t.Error("Block succeeded although the audit write failed")
	}
}

func TestImportCreatesAnAccountForEveryRow(t *testing.T) {
	// A group arrives as a list from the department, and creating thirty
	// accounts one request at a time is thirty chances to lose one.
	f := newFixture(t)
	svc, repo := f.service, f.repo

	result, err := svc.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor,
		Rows: []users.ImportRow{
			{Login: "s.popescu", FullName: "Sergiu Popescu"},
			{Login: "i.ivanov", FullName: "Ivan Ivanov", Email: "i@example.edu"},
		},
		Roles: []string{"student"},
	})
	if err != nil {
		t.Fatalf("Import() = %v", err)
	}

	if len(result.Created) != 2 {
		t.Fatalf("created %d accounts, want 2", len(result.Created))
	}
	for _, created := range result.Created {
		if created.OneTimePassword == "" {
			t.Errorf("%s got no password to hand over", created.User.Login)
		}
		if !created.User.MustChangePassword {
			t.Errorf("%s may keep the password an administrator saw", created.User.Login)
		}
	}
	if _, err := repo.ByLogin(context.Background(), "s.popescu"); err != nil {
		t.Errorf("the account was not stored: %v", err)
	}
}

func TestImportReportsTheRowsItCouldNotUse(t *testing.T) {
	// One duplicate must not reject the other twenty-nine, and whoever pasted
	// the list has to see which line to fix.
	f := newFixture(t)
	svc := f.service
	if _, err := svc.Create(context.Background(), users.CreateCommand{
		Login: "s.popescu", FullName: "Sergiu Popescu",
	}); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	result, err := svc.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor,
		Rows: []users.ImportRow{
			{Login: "s.popescu", FullName: "Sergiu Popescu"},
			{Login: "i.ivanov", FullName: "Ivan Ivanov"},
			{Login: "  ", FullName: "Nobody"},
		},
	})
	if err != nil {
		t.Fatalf("Import() = %v", err)
	}

	if len(result.Created) != 1 {
		t.Errorf("created %d accounts, want 1", len(result.Created))
	}
	reasons := map[string]string{}
	for _, skipped := range result.Skipped {
		reasons[skipped.Login] = skipped.Reason
	}
	if reasons["s.popescu"] != users.SkipLoginTaken {
		t.Errorf("the duplicate was skipped as %q, want %q", reasons["s.popescu"], users.SkipLoginTaken)
	}
	if len(result.Skipped) != 2 {
		t.Errorf("skipped %+v, want the duplicate and the empty row", result.Skipped)
	}
}

func TestImportRefusesARosterLargerThanAGroup(t *testing.T) {
	// Every row costs an argon2id hash, which is deliberately expensive. An
	// unbounded list is a way to spend the server's CPU with one request.
	f := newFixture(t)
	svc := f.service

	rows := make([]users.ImportRow, 501)
	for i := range rows {
		rows[i] = users.ImportRow{Login: fmt.Sprintf("s%d", i), FullName: "Student"}
	}

	_, err := svc.Import(context.Background(), users.ImportCommand{ActorID: f.actor, Rows: rows})

	if !errors.Is(err, users.ErrRosterTooLarge) {
		t.Errorf("Import() = %v, want ErrRosterTooLarge", err)
	}
}

func TestImportRecordsEachAccountItCreated(t *testing.T) {
	// Thirty accounts appearing at once with no trail is exactly the kind of
	// thing the trail exists for.
	f := newFixture(t)
	svc := f.service

	if _, err := svc.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor,
		Rows:    []users.ImportRow{{Login: "s.popescu", FullName: "Sergiu Popescu"}},
	}); err != nil {
		t.Fatalf("Import() = %v", err)
	}

	if !slices.Contains(f.sink.actions(), audit.ActionUserCreate) {
		t.Errorf("actions = %v, want a %s for the imported account",
			f.sink.actions(), audit.ActionUserCreate)
	}
}

func TestUpdatingAProfileRecordsWhatMoved(t *testing.T) {
	// It recorded the new name and nothing else — not what it replaced, and
	// not that the email had changed at all.
	f := newFixture(t)
	user := f.addUser(t, "s.popescu", "correct horse battery staple")

	if err := f.service.UpdateProfile(context.Background(), f.actor, user.ID,
		"Sergiu Popescu", "s.popescu@example.edu"); err != nil {
		t.Fatalf("UpdateProfile() = %v", err)
	}

	changes := lastChanges(t, f, audit.ActionUserUpdate)

	name, ok := changes["full_name"].(map[string]any)
	if !ok || name["from"] != "Test User" || name["to"] != "Sergiu Popescu" {
		t.Errorf("full_name = %v, want the previous name recorded", changes["full_name"])
	}
	if _, present := changes["email"]; !present {
		t.Errorf("changes = %v, want the email recorded", changes)
	}
}

func TestChangingRolesRecordsThemTheSameWayAsEverythingElse(t *testing.T) {
	// It already carried from/to under its own keys. One shape for every
	// change is what lets the panel render them without knowing the action.
	f := newFixture(t)
	user := f.addUser(t, "s.popescu", "correct horse battery staple")

	if err := f.service.ReplaceRoles(context.Background(), f.actor, user.ID,
		[]string{"organizer"}); err != nil {
		t.Fatalf("ReplaceRoles() = %v", err)
	}

	changes := lastChanges(t, f, audit.ActionUserRolesChange)

	if _, ok := changes["roles"].(map[string]any); !ok {
		t.Errorf("changes = %v, want the roles under one shape", changes)
	}
}

// lastChanges returns the change set of the newest entry with that action.
func lastChanges(t *testing.T, f *fixture, action string) map[string]any {
	t.Helper()

	for i := len(f.sink.entries) - 1; i >= 0; i-- {
		if f.sink.entries[i].Action != action {
			continue
		}
		changes, ok := f.sink.entries[i].Payload["changes"].(map[string]any)
		if !ok {
			t.Fatalf("%s payload = %v, want a changes map", action, f.sink.entries[i].Payload)
		}
		return changes
	}
	t.Fatalf("no %s entry among %v", action, f.sink.actions())
	return nil
}

func TestTheLastAdministratorCannotBeDemoted(t *testing.T) {
	// The lockout this guards. An administrator opens their own account,
	// unchecks "admin" and saves; now nobody in the installation holds
	// users.manage, so nobody can put it back. `bootstrap` does not help — it
	// returns early for a login that exists and never looks at what roles the
	// account still has — so recovery is hand-written SQL against production.
	//
	// The screen made this two clicks away. It was always reachable through
	// the API, which is why the rule belongs here and not in the interface.
	f := newFixture(t)
	ctx := context.Background()
	admin := f.addUser(t, "root", "some password")
	if err := f.repo.ReplaceRoles(ctx, admin.ID, []string{users.RoleAdmin}); err != nil {
		t.Fatalf("ReplaceRoles() = %v", err)
	}

	err := f.service.ReplaceRoles(ctx, admin.ID, admin.ID, []string{"student"})

	if !errors.Is(err, users.ErrLastAdministrator) {
		t.Errorf("ReplaceRoles() = %v, want it to refuse the last administrator", err)
	}
}

func TestAnAdministratorMayBeDemotedWhileAnotherRemains(t *testing.T) {
	// The guard must protect the installation without freezing its staff: two
	// administrators is the ordinary state, and removing one of them is an
	// ordinary act.
	f := newFixture(t)
	ctx := context.Background()
	first := f.addUser(t, "root", "some password")
	second := f.addUser(t, "dean", "another password")
	for _, id := range []uuid.UUID{first.ID, second.ID} {
		if err := f.repo.ReplaceRoles(ctx, id, []string{users.RoleAdmin}); err != nil {
			t.Fatalf("ReplaceRoles() = %v", err)
		}
	}

	if err := f.service.ReplaceRoles(ctx, first.ID, second.ID, []string{"student"}); err != nil {
		t.Errorf("ReplaceRoles() = %v, want the demotion to be allowed", err)
	}
}

func TestTheLastAdministratorCannotBeBlocked(t *testing.T) {
	// The same lockout by the other door. Self-blocking is already refused,
	// but two administrators can block each other down to none.
	f := newFixture(t)
	ctx := context.Background()
	admin := f.addUser(t, "root", "some password")
	other := f.addUser(t, "dean", "another password")
	if err := f.repo.ReplaceRoles(ctx, admin.ID, []string{users.RoleAdmin}); err != nil {
		t.Fatalf("ReplaceRoles() = %v", err)
	}

	err := f.service.Block(ctx, other.ID, admin.ID, "some reason")

	if !errors.Is(err, users.ErrLastAdministrator) {
		t.Errorf("Block() = %v, want it to refuse the last administrator", err)
	}
}

func TestCreateRefusesAccountDetailsItCannotStore(t *testing.T) {
	// Each of these used to surface as an undeclared error, which the HTTP
	// layer could only answer with a 500; now they are one sentinel the
	// handler maps to a bad request.
	f := newFixture(t)
	cases := map[string]users.CreateCommand{
		"empty login":    {FullName: "Somebody"},
		"empty name":     {Login: "somebody"},
		"overlong login": {Login: strings.Repeat("a", users.MaxLoginLength+1), FullName: "Somebody"},
		"overlong name":  {Login: "somebody", FullName: strings.Repeat("n", users.MaxFullNameLength+1)},
		"not an email":   {Login: "somebody", FullName: "Somebody", Email: "not-an-address"},
		"email with a display name": {
			Login: "somebody", FullName: "Somebody", Email: "Somebody <s@example.edu>",
		},
	}

	for name, cmd := range cases {
		cmd.ActorID = f.actor
		if _, err := f.service.Create(context.Background(), cmd); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("%s: Create() = %v, want ErrInvalidAccount", name, err)
		}
	}
}

func TestUpdateProfileRefusesAMalformedEmail(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "correct horse battery staple")

	err := f.service.UpdateProfile(context.Background(), f.actor, user.ID, "Pyotr Petrov", "nope@")

	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("UpdateProfile() = %v, want ErrInvalidAccount", err)
	}
}

// Delete and Restore are wrappers over BulkSetStatus with a selection of one
// (see setStatus in service.go), so the guards below are the same ones
// bulk_test.go exercises for a selection: what is new here is only that a
// refusal comes back as the sentinel rather than a skip, because for a single
// account the skip is the whole outcome.

func TestDeleteRefusesTheLastAdministrator(t *testing.T) {
	f := newBulkFixture(t)
	only := f.createAdmin(t, "admin-one")

	err := f.service.Delete(context.Background(), f.admin.ID, only.ID, "left the university")

	if !errors.Is(err, users.ErrLastAdministrator) {
		t.Errorf("Delete() = %v, want ErrLastAdministrator", err)
	}
}

func TestDeleteRefusesYourself(t *testing.T) {
	f := newBulkFixture(t)

	err := f.service.Delete(context.Background(), f.admin.ID, f.admin.ID, "why not")

	if !errors.Is(err, users.ErrCannotActOnSelf) {
		t.Errorf("Delete() = %v, want ErrCannotActOnSelf", err)
	}
}

func TestDeleteRetiresTheSessions(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	before := target.SessionGeneration

	if err := f.service.Delete(context.Background(), f.admin.ID, target.ID, "graduated"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	after, err := f.repo.ByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("ByID() returned error: %v", err)
	}
	if after.Status != users.StatusDeleted {
		t.Errorf("Status = %q, want deleted", after.Status)
	}
	if after.SessionGeneration <= before {
		t.Error("the session generation was not advanced, so open sessions survive")
	}
}

func TestDeleteRequiresAReason(t *testing.T) {
	// Deletion is answered to exactly as blocking is: "no reason given" is not
	// an answer the trail can carry.
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")

	err := f.service.Delete(context.Background(), f.admin.ID, target.ID, "   ")

	if !errors.Is(err, users.ErrReasonRequired) {
		t.Errorf("Delete() = %v, want ErrReasonRequired", err)
	}
}

func TestDeleteIsAudited(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")

	_ = f.service.Delete(context.Background(), f.admin.ID, target.ID, "graduated")

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserDelete {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserDelete)
	}
}

func TestRestoreBringsBackADeletedAccount(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, target.ID, "mistake"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	if err := f.service.Restore(context.Background(), f.admin.ID, target.ID); err != nil {
		t.Fatalf("Restore() returned error: %v", err)
	}

	after, err := f.repo.ByID(context.Background(), target.ID)
	if err != nil {
		t.Fatalf("ByID() returned error: %v", err)
	}
	if after.Status != users.StatusActive {
		t.Errorf("Status = %q, want active", after.Status)
	}
}

func TestRestoreIsAudited(t *testing.T) {
	f := newBulkFixture(t)
	target := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, target.ID, "mistake"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	f.sink.entries = nil // isolate what Restore itself records

	_ = f.service.Restore(context.Background(), f.admin.ID, target.ID)

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserRestore {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserRestore)
	}
}

func TestRestoreRefusesWhenTheLoginWasTaken(t *testing.T) {
	// The direct price of releasing the login on deletion: it can be taken by
	// somebody else before the account comes back.
	f := newBulkFixture(t)
	gone := f.createUser(t, "ivanov")
	if err := f.service.Delete(context.Background(), f.admin.ID, gone.ID, "mistake"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	f.createUser(t, "ivanov")

	err := f.service.Restore(context.Background(), f.admin.ID, gone.ID)

	if !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("Restore() = %v, want ErrLoginTaken", err)
	}
}

func TestRestoreRefusesWhenTheEmailWasTaken(t *testing.T) {
	// The same price, paid on the other field a deleted account releases.
	f := newBulkFixture(t)
	gone := f.repo.Add(users.User{
		Login: "gone-by-email", Email: "shared@example.com", FullName: "Gone", PasswordHash: "x",
	})
	if err := f.service.Delete(context.Background(), f.admin.ID, gone.ID, "mistake"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}
	f.repo.Add(users.User{Login: "someone-else", Email: "shared@example.com", FullName: "Live", PasswordHash: "x"})

	err := f.service.Restore(context.Background(), f.admin.ID, gone.ID)

	if !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("Restore() = %v, want ErrEmailTaken", err)
	}
}

// busyService is a service whose only hashing slot is already taken, with a
// sign-in wait short enough that a test can tell it from the longer wait the
// administrative paths are given.
func busyService(t *testing.T, repo *userstest.Repository) (*users.Service, *password.Hasher, func()) {
	t.Helper()
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	release, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	t.Cleanup(release)
	return users.NewService(repo, audit.New(&collectingSink{}), &userstest.SpyUnitOfWork{}, hasher), hasher, release
}

func TestChangePasswordReportsBusyHashingRatherThanAWrongPassword(t *testing.T) {
	// A refusal for load is not a verdict on the password. Reporting it as
	// ErrWrongPassword would tell somebody who typed their password correctly
	// that they did not.
	f := newFixture(t)
	user := f.addUser(t, "petrov", "old password")
	service, _, _ := busyService(t, f.repo)

	err := service.ChangePassword(context.Background(), users.ChangePasswordCommand{
		UserID: user.ID, OldPassword: "old password", NewPassword: "a brand new password",
	})

	if !errors.Is(err, password.ErrBusy) {
		t.Errorf("err = %v, want password.ErrBusy", err)
	}
	if errors.Is(err, users.ErrWrongPassword) {
		t.Error("a refusal for load was reported as a wrong password")
	}
}

func TestCreateReportsBusyHashingWhenNoSlotComesFree(t *testing.T) {
	f := newFixture(t)
	service, _, _ := busyService(t, f.repo)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := service.Create(ctx, users.CreateCommand{ActorID: f.actor, Login: "petrov", FullName: "Pyotr Petrov"})

	if !errors.Is(err, password.ErrBusy) {
		t.Errorf("err = %v, want password.ErrBusy", err)
	}
	if _, err := f.repo.ByLogin(context.Background(), "petrov"); !errors.Is(err, users.ErrNotFound) {
		t.Error("an account was stored without a password digest")
	}
}

func TestImportWaitsLongerThanASignInForAHashingSlot(t *testing.T) {
	// An administrator's roster is authenticated, bounded and legitimate, and
	// a refusal halfway through it discards the one-time passwords already
	// issued. So it waits well past the sign-in wait rather than giving up
	// the moment anonymous traffic fills the slots.
	f := newFixture(t)
	service, _, release := busyService(t, f.repo)
	time.AfterFunc(200*time.Millisecond, release)

	result, err := service.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor,
		Rows:    []users.ImportRow{{Login: "petrov", FullName: "Pyotr Petrov"}},
	})

	if err != nil {
		t.Fatalf("Import() = %v, want it to wait for the slot", err)
	}
	if len(result.Created) != 1 {
		t.Errorf("created %d accounts, want 1", len(result.Created))
	}
}
