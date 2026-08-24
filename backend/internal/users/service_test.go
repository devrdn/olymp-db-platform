package users_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// collectingSink keeps audit entries for assertions.
type collectingSink struct{ entries []audit.Entry }

func (s *collectingSink) Append(_ context.Context, e audit.Entry) error {
	s.entries = append(s.entries, e)
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
	actor   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := userstest.New()
	sink := &collectingSink{}
	return &fixture{
		service: users.NewService(repo, audit.New(sink)),
		repo:    repo,
		sink:    sink,
		actor:   uuid.New(),
	}
}

// addUser stores an account with a known password.
func (f *fixture) addUser(t *testing.T, login, plaintext string) users.User {
	t.Helper()
	hash, err := password.Hash(plaintext)
	if err != nil {
		t.Fatalf("password.Hash() returned error: %v", err)
	}
	return f.repo.Add(users.User{Login: login, FullName: "Test User", PasswordHash: hash})
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

	ok, err := password.Verify(stored.PasswordHash, created.OneTimePassword)
	if err != nil || !ok {
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

	if err := f.service.Block(context.Background(), f.actor, user.ID); err != nil {
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

	_ = f.service.Block(context.Background(), f.actor, user.ID)

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionUserBlock {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionUserBlock)
	}
}

func TestAdministratorsCannotBlockThemselves(t *testing.T) {
	// Locking the last administrator out of the installation is an easy
	// mistake and an expensive one to undo.
	f := newFixture(t)
	user := f.addUser(t, "admin", "some password")

	err := f.service.Block(context.Background(), user.ID, user.ID)

	if !errors.Is(err, users.ErrCannotActOnSelf) {
		t.Errorf("err = %v, want ErrCannotActOnSelf", err)
	}
}

func TestUnblockRestoresAccess(t *testing.T) {
	f := newFixture(t)
	user := f.addUser(t, "petrov", "some password")
	_ = f.service.Block(context.Background(), f.actor, user.ID)

	if err := f.service.Unblock(context.Background(), f.actor, user.ID); err != nil {
		t.Fatalf("Unblock() returned error: %v", err)
	}

	after, _ := f.repo.Get(user.ID)
	if after.Status != users.StatusActive {
		t.Errorf("Status = %q, want active", after.Status)
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
	if ok, _ := password.Verify(after.PasswordHash, "a brand new password"); !ok {
		t.Error("the new password does not verify against the stored digest")
	}
	if ok, _ := password.Verify(after.PasswordHash, "old password"); ok {
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
	if ok, _ := password.Verify(after.PasswordHash, issued); !ok {
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
	if entry.Payload["from"] == nil || entry.Payload["to"] == nil {
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

	if err := f.service.Block(ctx, f.actor, missing); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("Block() = %v, want ErrNotFound", err)
	}
	if _, err := f.service.ResetPassword(ctx, f.actor, missing); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ResetPassword() = %v, want ErrNotFound", err)
	}
}

func mustHash(t *testing.T, plaintext string) string {
	t.Helper()
	hash, err := password.Hash(plaintext)
	if err != nil {
		t.Fatalf("password.Hash() returned error: %v", err)
	}
	return hash
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
	if ok, _ := password.Verify(stored.PasswordHash, first.OneTimePassword); !ok {
		t.Error("the existing administrator's password was replaced")
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
