package auth

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
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
	service *Service
	repo    *userstest.Repository
	sink    *collectingSink
	user    users.User
}

const testPassword = "correct horse battery staple"

func newFixture(t *testing.T) *fixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	sink := &collectingSink{}
	hash, err := password.Hash(testPassword)
	if err != nil {
		t.Fatalf("password.Hash() returned error: %v", err)
	}
	user := repo.Add(users.User{
		Login:        "ivanov",
		FullName:     "Ivan Ivanov",
		PasswordHash: hash,
		Status:       users.StatusActive,
	})

	service := NewService(ServiceConfig{
		Users:    repo,
		Sessions: NewSessionStore(c, time.Hour),
		Audit:    audit.New(sink),
		Limiter:  NewLimiter(c),
		Logger:   logging.New("error", io.Discard),
	})

	return &fixture{service: service, repo: repo, sink: sink, user: user}
}

func loginCmd(password string) LoginCommand {
	return LoginCommand{Login: "ivanov", Password: password, IP: "10.0.0.1"}
}

func TestLoginIssuesASessionForValidCredentials(t *testing.T) {
	f := newFixture(t)

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if err != nil {
		t.Fatalf("Login() returned error: %v", err)
	}
	if result.Token == "" {
		t.Error("Login() issued no session token")
	}
	if result.User.ID != f.user.ID {
		t.Errorf("User.ID = %v, want %v", result.User.ID, f.user.ID)
	}
}

func TestLoginRejectsAWrongPassword(t *testing.T) {
	f := newFixture(t)

	result, err := f.service.Login(context.Background(), loginCmd("wrong password"))

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
	if result.Token != "" {
		t.Error("a session was issued despite a wrong password")
	}
}

func TestUnknownLoginAndWrongPasswordAreIndistinguishable(t *testing.T) {
	// Different answers would turn the login form into a directory of who has
	// an account here.
	f := newFixture(t)
	ctx := context.Background()

	_, wrongPassword := f.service.Login(ctx, loginCmd("wrong password"))
	_, unknownUser := f.service.Login(ctx, LoginCommand{Login: "nobody", Password: "whatever", IP: "10.0.0.2"})

	if wrongPassword.Error() != unknownUser.Error() {
		t.Errorf("errors differ:\n  wrong password: %v\n  unknown login:  %v", wrongPassword, unknownUser)
	}
}

func TestUnknownLoginStillSpendsTheHashingTime(t *testing.T) {
	// Returning early for an unknown login would answer in microseconds while
	// a real account costs tens of milliseconds — a timing oracle for account
	// enumeration.
	f := newFixture(t)

	started := time.Now()
	_, _ = f.service.Login(context.Background(), LoginCommand{Login: "nobody", Password: "whatever", IP: "10.0.0.2"})
	elapsed := time.Since(started)

	if elapsed < 10*time.Millisecond {
		t.Errorf("unknown login answered in %v; it skipped the password comparison", elapsed)
	}
}

func TestBlockedAccountIsRejectedEvenWithTheRightPassword(t *testing.T) {
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), f.user.ID, users.StatusBlocked)

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if !errors.Is(err, ErrAccountBlocked) {
		t.Errorf("err = %v, want ErrAccountBlocked", err)
	}
	if result.Token != "" {
		t.Error("a blocked account received a session")
	}
}

func TestBlockedAccountLooksLikeAnyOtherFailureToSomeoneGuessing(t *testing.T) {
	// The account owner deserves to be told they are blocked, but only after
	// proving they own it. A wrong guess must not reveal that the login exists.
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), f.user.ID, users.StatusBlocked)

	_, err := f.service.Login(context.Background(), loginCmd("wrong password"))

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want the generic ErrInvalidCredentials for a wrong guess", err)
	}
}

func TestLoginRecordsTheSessionGenerationOfTheAccount(t *testing.T) {
	f := newFixture(t)
	generation, _ := f.repo.BumpSessionGeneration(context.Background(), f.user.ID)

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))
	if err != nil {
		t.Fatalf("Login() returned error: %v", err)
	}

	session, err := f.service.Sessions().Get(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("session lookup returned error: %v", err)
	}
	if session.Generation != generation {
		t.Errorf("session generation = %d, want the account's %d", session.Generation, generation)
	}
}

func TestLoginStampsTheLastLoginTime(t *testing.T) {
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd(testPassword))

	stored, _ := f.repo.Get(f.user.ID)
	if stored.LastLoginAt == nil {
		t.Error("the successful login was not stamped on the account")
	}
}

func TestLoginSurfacesAOneTimePassword(t *testing.T) {
	// An administrator-issued password has to end in the user choosing theirs.
	f := newFixture(t)
	hash, _ := password.Hash(testPassword)
	_ = f.repo.SetPassword(context.Background(), f.user.ID, hash, true)

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if err != nil {
		t.Fatalf("Login() returned error: %v", err)
	}
	if !result.MustChangePassword {
		t.Error("the result does not tell the client a password change is required")
	}
}

func TestSuccessfulLoginIsAudited(t *testing.T) {
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd(testPassword))

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLogin {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionAuthLogin)
	}
	if f.sink.entries[0].ActorID == nil || *f.sink.entries[0].ActorID != f.user.ID {
		t.Error("the audit entry does not name the account that logged in")
	}
}

func TestFailedLoginIsAudited(t *testing.T) {
	// Failed attempts are the signal that matters when investigating later.
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd("wrong password"))

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLoginFailed {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionAuthLoginFailed)
	}
}

func TestAuditNeverCarriesTheAttemptedPassword(t *testing.T) {
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd("hunter2"))

	for _, entry := range f.sink.entries {
		for key, value := range entry.Payload {
			if str, ok := value.(string); ok && str == "hunter2" {
				t.Errorf("audit payload field %q carries the attempted password", key)
			}
		}
	}
}

func TestRepeatedFailuresAreBlocked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var lastErr error
	for range maxLoginAttemptsPerAccount + 1 {
		_, lastErr = f.service.Login(ctx, loginCmd("wrong password"))
	}

	if !errors.Is(lastErr, ErrTooManyAttempts) {
		t.Errorf("err = %v, want ErrTooManyAttempts after repeated failures", lastErr)
	}
}

func TestThrottlingSurvivesTheCorrectPassword(t *testing.T) {
	// Otherwise an attacker who eventually guesses right walks straight in,
	// which is exactly the case throttling exists for.
	f := newFixture(t)
	ctx := context.Background()
	for range maxLoginAttemptsPerAccount + 1 {
		_, _ = f.service.Login(ctx, loginCmd("wrong password"))
	}

	_, err := f.service.Login(ctx, loginCmd(testPassword))

	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("err = %v, want the throttle to hold even for correct credentials", err)
	}
}

func TestSuccessClearsTheFailureCount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, _ = f.service.Login(ctx, loginCmd("wrong password"))
	_, _ = f.service.Login(ctx, loginCmd("wrong password"))

	if _, err := f.service.Login(ctx, loginCmd(testPassword)); err != nil {
		t.Fatalf("Login() returned error: %v", err)
	}

	// The counter is clear, so a fresh run of failures is allowed again.
	for range maxLoginAttemptsPerAccount {
		if _, err := f.service.Login(ctx, loginCmd("wrong password")); errors.Is(err, ErrTooManyAttempts) {
			t.Fatal("the failure counter was not cleared by the successful login")
		}
	}
}

func TestLoginUpgradesAnOutdatedPasswordHash(t *testing.T) {
	// Raising the cost must not lock anyone out: a digest made with weaker
	// parameters still authenticates, and is replaced on the way through.
	f := newFixture(t)
	ctx := context.Background()
	outdated := passwordtest.WeakHash(t, testPassword)
	_ = f.repo.SetPassword(ctx, f.user.ID, outdated, false)

	if _, err := f.service.Login(ctx, loginCmd(testPassword)); err != nil {
		t.Fatalf("Login() rejected a password stored with older parameters: %v", err)
	}

	stored, _ := f.repo.Get(f.user.ID)
	if stored.PasswordHash == outdated {
		t.Error("the outdated digest was left in place after a successful login")
	}
	if password.NeedsRehash(stored.PasswordHash) {
		t.Error("the replacement digest still uses outdated parameters")
	}
}

func TestLoginIsCaseInsensitiveInTheLogin(t *testing.T) {
	// The unique index is on lower(login); authentication must agree with it.
	f := newFixture(t)

	_, err := f.service.Login(context.Background(), LoginCommand{Login: "IVANOV", Password: testPassword, IP: "10.0.0.1"})

	if err != nil {
		t.Errorf("Login() with a differently cased login = %v, want success", err)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	result, _ := f.service.Login(ctx, loginCmd(testPassword))

	if err := f.service.Logout(ctx, result.Token); err != nil {
		t.Fatalf("Logout() returned error: %v", err)
	}

	if _, err := f.service.Sessions().Get(ctx, result.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Error("the session survived logout")
	}
}

func TestLogoutIsAudited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	result, _ := f.service.Login(ctx, loginCmd(testPassword))
	f.sink.entries = nil

	_ = f.service.Logout(ctx, result.Token)

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLogout {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionAuthLogout)
	}
}

func TestLogoutOfAnUnknownSessionIsNotAnError(t *testing.T) {
	f := newFixture(t)

	if err := f.service.Logout(context.Background(), "stale-cookie"); err != nil {
		t.Errorf("Logout() with a stale token = %v, want nil", err)
	}
}

func TestAddressThrottleIsSpentBeforeAccountCountersAreCreated(t *testing.T) {
	// Every login somebody types becomes a counter key, so a caller whose
	// address is already refused must not be able to keep minting new ones by
	// inventing logins: on the in-process cache that is how the store fills
	// until every counter — and every sign-in — is refused.
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	service := NewService(ServiceConfig{
		Users:                 userstest.New(),
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(&collectingSink{}),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		MaxAttemptsPerAddress: 2,
	})
	ctx := context.Background()

	for i := range 10 {
		_, _ = service.Login(ctx, LoginCommand{
			Login: "invented-" + string(rune('a'+i)), Password: "whatever", IP: "10.0.0.9",
		})
	}

	// One address counter, plus an account counter for each of the two attempts
	// the address was allowed. The eight refused attempts left nothing behind.
	if got := c.Len(); got != 3 {
		t.Errorf("the cache holds %d counters, want 3: refused attempts created account keys", got)
	}
}

func TestPasswordChangeIsThrottledPerAccount(t *testing.T) {
	// The endpoint verifies a password exactly as sign-in does, so a borrowed
	// session must not be a place to guess the current one at leisure.
	f := newFixture(t)
	ctx := context.Background()

	for range maxPasswordChangeAttempts {
		if err := f.service.AllowPasswordChange(ctx, f.user.ID); err != nil {
			t.Fatalf("AllowPasswordChange() refused within the limit: %v", err)
		}
	}
	if err := f.service.AllowPasswordChange(ctx, f.user.ID); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("err = %v, want ErrTooManyAttempts once the window is spent", err)
	}

	// A success forgets the attempts, as it does at sign-in.
	f.service.ClearPasswordChangeThrottle(ctx, f.user.ID)
	if err := f.service.AllowPasswordChange(ctx, f.user.ID); err != nil {
		t.Errorf("AllowPasswordChange() after a reset = %v, want nil", err)
	}
}
