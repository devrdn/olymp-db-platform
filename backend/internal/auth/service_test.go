package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/monitor"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// collectingSink guards appends, since some tests sign in concurrently.
type collectingSink struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (s *collectingSink) Append(_ context.Context, e audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *collectingSink) AppendMany(_ context.Context, entries []audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entries...)
	return nil
}

func (s *collectingSink) actions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	hash := passwordtest.Hash(t, testPassword)
	user := repo.Add(users.User{
		Login:        "ivanov",
		FullName:     "Ivan Ivanov",
		PasswordHash: hash,
		Status:       users.StatusActive,
	})

	service := NewService(ServiceConfig{
		Users:     repo,
		Sessions:  NewSessionStore(c, time.Hour),
		Audit:     audit.New(sink),
		Limiter:   NewLimiter(c),
		Logger:    logging.New("error", io.Discard),
		Passwords: passwordtest.NewHasher(),
		Devices:   testDevices(t),
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
	f := newFixture(t)
	ctx := context.Background()

	_, wrongPassword := f.service.Login(ctx, loginCmd("wrong password"))
	_, unknownUser := f.service.Login(ctx, LoginCommand{Login: "nobody", Password: "whatever", IP: "10.0.0.2"})

	if wrongPassword.Error() != unknownUser.Error() {
		t.Errorf("errors differ:\n  wrong password: %v\n  unknown login:  %v", wrongPassword, unknownUser)
	}
}

func TestUnknownLoginStillSpendsTheHashingTime(t *testing.T) {
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
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if !errors.Is(err, ErrAccountBlocked) {
		t.Errorf("err = %v, want ErrAccountBlocked", err)
	}
	if result.Token != "" {
		t.Error("a blocked account received a session")
	}
}

func TestDeletedAccountIsRejectedEvenWithTheRightPassword(t *testing.T) {
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusDeleted, users.StatusChange{})

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if !errors.Is(err, ErrAccountBlocked) {
		t.Errorf("err = %v, want ErrAccountBlocked", err)
	}
	if result.Token != "" {
		t.Error("a deleted account received a session")
	}
}

func TestBlockedAccountLooksLikeAnyOtherFailureToSomeoneGuessing(t *testing.T) {
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

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
	f := newFixture(t)
	hash := passwordtest.Hash(t, testPassword)
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
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd("wrong password"))

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLoginFailed {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionAuthLoginFailed)
	}
	if reason := f.sink.entries[0].Payload["reason"]; reason != ReasonInvalidCredentials {
		t.Errorf("reason = %v, want %q", reason, ReasonInvalidCredentials)
	}
}

func TestBlockedAccountFailureRecordsWhyItIsBlocked(t *testing.T) {
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

	_, _ = f.service.Login(context.Background(), loginCmd(testPassword))

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLoginFailed {
		t.Fatalf("audit actions = %v, want one %q", got, audit.ActionAuthLoginFailed)
	}
	if reason := f.sink.entries[0].Payload["reason"]; reason != ReasonAccountBlocked {
		t.Errorf("reason = %v, want %q", reason, ReasonAccountBlocked)
	}
}

// The trail must not become the oracle the endpoint denies.
func TestBlockedAccountWithWrongPasswordNeverRecordsTheBlock(t *testing.T) {
	f := newFixture(t)
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

	_, err := f.service.Login(context.Background(), loginCmd("wrong password"))

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err = %v, want ErrInvalidCredentials", err)
	}
	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLoginFailed {
		t.Fatalf("audit actions = %v, want one %q", got, audit.ActionAuthLoginFailed)
	}
	if reason := f.sink.entries[0].Payload["reason"]; reason != ReasonInvalidCredentials {
		t.Errorf("reason = %v, want %q (never %q, for a password that never matched)",
			reason, ReasonInvalidCredentials, ReasonAccountBlocked)
	}
}

func TestThrottledLoginRecordsTooManyAttempts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var lastErr error
	for range maxLoginAttemptsPerAccountAddress + 1 {
		_, lastErr = f.service.Login(ctx, loginCmd("wrong password"))
	}
	if !errors.Is(lastErr, ErrTooManyAttempts) {
		t.Fatalf("err = %v, want ErrTooManyAttempts", lastErr)
	}

	last := f.sink.entries[len(f.sink.entries)-1]
	if last.Action != audit.ActionAuthLoginFailed {
		t.Fatalf("last recorded action = %q, want %q", last.Action, audit.ActionAuthLoginFailed)
	}
	if reason := last.Payload["reason"]; reason != ReasonTooManyAttempts {
		t.Errorf("reason = %v, want %q", reason, ReasonTooManyAttempts)
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
	for range maxLoginAttemptsPerAccountAddress + 1 {
		_, lastErr = f.service.Login(ctx, loginCmd("wrong password"))
	}

	if !errors.Is(lastErr, ErrTooManyAttempts) {
		t.Errorf("err = %v, want ErrTooManyAttempts after repeated failures", lastErr)
	}
}

func TestThrottlingSurvivesTheCorrectPassword(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for range maxLoginAttemptsPerAccountAddress + 1 {
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

	for range maxLoginAttemptsPerAccountAddress {
		if _, err := f.service.Login(ctx, loginCmd("wrong password")); errors.Is(err, ErrTooManyAttempts) {
			t.Fatal("the failure counter was not cleared by the successful login")
		}
	}
}

func TestLoginUpgradesAnOutdatedPasswordHash(t *testing.T) {
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
	// CLAUDE.md rule 5.
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	service := NewService(ServiceConfig{
		Users:                 userstest.New(),
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(&collectingSink{}),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             passwordtest.NewHasher(),
		Devices:               testDevices(t),
		MaxAttemptsPerAddress: 2,
	})
	ctx := context.Background()

	for i := range 10 {
		_, _ = service.Login(ctx, LoginCommand{
			Login: "invented-" + string(rune('a'+i)), Password: "whatever", IP: "10.0.0.9",
		})
	}

	// One address counter plus two account counters for each of the two
	// allowed attempts; refused attempts left nothing.
	if got := c.Len(); got != 5 {
		t.Errorf("the cache holds %d counters, want 5: refused attempts created account keys", got)
	}
}

func TestAnOverlongLoginNeverBecomesARateLimitKey(t *testing.T) {
	// An over-long login mints no counter, but the address counter is still
	// spent: c.Len() == 1, not 0.
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	service := NewService(ServiceConfig{
		Users:     userstest.New(),
		Sessions:  NewSessionStore(c, time.Hour),
		Audit:     audit.New(&collectingSink{}),
		Limiter:   NewLimiter(c),
		Logger:    logging.New("error", io.Discard),
		Passwords: passwordtest.NewHasher(),
		Devices:   testDevices(t),
	})

	overlong := strings.Repeat("a", users.MaxLoginLength+1)
	_, err := service.Login(context.Background(), LoginCommand{
		Login: overlong, Password: "whatever", IP: "10.0.0.9",
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() with an overlong login = %v, want ErrInvalidCredentials", err)
	}
	if got := c.Len(); got != 1 {
		t.Errorf("the cache holds %d counters, want 1 (the address only) — "+
			"the overlong login minted a counter of its own", got)
	}
}

func TestPasswordChangeIsThrottledPerAccount(t *testing.T) {
	// CLAUDE.md rule 4.
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

	f.service.ClearPasswordChangeThrottle(ctx, f.user.ID)
	if err := f.service.AllowPasswordChange(ctx, f.user.ID); err != nil {
		t.Errorf("AllowPasswordChange() after a reset = %v, want nil", err)
	}
}

func TestASignInThatCannotGetAHashingSlotIsRefusedAndStillCounted(t *testing.T) {
	// A busy refusal has already spent the address budget, so it is not
	// free to retry.
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})

	const wait = 50 * time.Millisecond
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: wait})
	sink := &collectingSink{}
	service := NewService(ServiceConfig{
		Users:                 repo,
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(sink),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             hasher,
		Devices:               testDevices(t),
		MaxAttemptsPerAddress: 1,
	})

	slot, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}

	started := time.Now()
	_, err = service.Login(context.Background(), loginCmd(testPassword))
	elapsed := time.Since(started)
	slot.Release()

	if !errors.Is(err, password.ErrBusy) {
		t.Fatalf("Login() with every hashing slot held = %v, want password.ErrBusy", err)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Error("a refusal for load was reported as wrong credentials")
	}
	if elapsed > wait+time.Second {
		t.Errorf("Login() took %v, far past the %v wait", elapsed, wait)
	}
	for _, entry := range sink.entries {
		if entry.Action == audit.ActionAuthLoginFailed && entry.Payload["reason"] == ReasonInvalidCredentials {
			t.Error("the refused attempt was recorded as a wrong password")
		}
	}

	if _, err := service.Login(context.Background(), loginCmd(testPassword)); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("the next attempt = %v, want ErrTooManyAttempts: the refusal was not counted", err)
	}
}

func TestAThrottledAttemptNeverStoresAnOversizedLogin(t *testing.T) {
	// A refusal by the address budget is recorded before the length guard,
	// so the stored login must be bounded on its own.
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	sink := &collectingSink{}
	service := NewService(ServiceConfig{
		Users:                 userstest.New(),
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(sink),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             passwordtest.NewHasher(),
		Devices:               testDevices(t),
		MaxAttemptsPerAddress: 1,
	})
	ctx := context.Background()
	_, _ = service.Login(ctx, LoginCommand{Login: "someone", Password: "whatever", IP: "10.0.0.9"})

	oversized := strings.Repeat("ф", 450_000) // 900 kB of two-byte runes
	_, err := service.Login(ctx, LoginCommand{Login: oversized, Password: "whatever", IP: "10.0.0.9"})

	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("Login() = %v, want ErrTooManyAttempts", err)
	}
	last := sink.entries[len(sink.entries)-1]
	stored, _ := last.Payload["login"].(string)
	if len(stored) > users.MaxLoginLength {
		t.Errorf("the trail stored a %d-byte login, want at most %d", len(stored), users.MaxLoginLength)
	}
	if !utf8.ValidString(stored) {
		t.Error("the bounded login is not valid UTF-8: it was cut inside a character")
	}
}

func TestAnOverlongPasswordIsRefusedBeforeAnyAccountCounterOrHash(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	service := NewService(ServiceConfig{
		Users:     userstest.New(),
		Sessions:  NewSessionStore(c, time.Hour),
		Audit:     audit.New(&collectingSink{}),
		Limiter:   NewLimiter(c),
		Logger:    logging.New("error", io.Discard),
		Passwords: hasher,
		Devices:   testDevices(t),
	})
	slot, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	defer slot.Release()

	_, err = service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: strings.Repeat("a", password.MaxLength+1), IP: "10.0.0.9",
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() with an overlong password = %v, want ErrInvalidCredentials", err)
	}
	if got := c.Len(); got != 1 {
		t.Errorf("the cache holds %d counters, want 1 (the address only)", got)
	}
}

func throttleService(t *testing.T, ceiling int) *Service {
	t.Helper()
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})
	return NewService(ServiceConfig{
		Users:                 repo,
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(&collectingSink{}),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             passwordtest.NewHasher(),
		Devices:               testDevices(t),
		MaxAttemptsPerAccount: ceiling,
	})
}

func TestAGuesserAtAnotherAddressCannotLockTheOwnerOut(t *testing.T) {
	service := throttleService(t, 0)
	ctx := context.Background()

	var lastErr error
	for range maxLoginAttemptsPerAccountAddress + 5 {
		_, lastErr = service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.66"})
	}
	if !errors.Is(lastErr, ErrTooManyAttempts) {
		t.Fatalf("the guesser's last attempt = %v, want ErrTooManyAttempts", lastErr)
	}

	if _, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); err != nil {
		t.Errorf("the owner at another address = %v, want a session", err)
	}
}

func TestTheAccountWideCeilingStopsAGuessSpreadAcrossAddresses(t *testing.T) {
	const ceiling = 5
	service := throttleService(t, ceiling)
	ctx := context.Background()

	for i := range ceiling {
		ip := fmt.Sprintf("10.0.1.%d", i)
		if _, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: ip}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v, want ErrInvalidCredentials within the ceiling", i, err)
		}
	}

	_, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.2.1"})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("an attempt past the account-wide ceiling = %v, want ErrTooManyAttempts", err)
	}
}

func TestAnAttemptRefusedAtItsAddressDoesNotSpendTheAccountCeiling(t *testing.T) {
	const ceiling = maxLoginAttemptsPerAccountAddress + 2
	service := throttleService(t, ceiling)
	ctx := context.Background()

	for range 3 * ceiling {
		_, _ = service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.66"})
	}

	if _, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); err != nil {
		t.Errorf("the owner after one address's refused attempts = %v, want a session", err)
	}
}

func TestBusyRefusalsNeverSpendTheAccountsOwnCounters(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 10 * time.Millisecond})
	const busyAttempts = maxLoginAttemptsPerAccountAddress + 2
	service := NewService(ServiceConfig{
		Users:                 repo,
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(&collectingSink{}),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             hasher,
		Devices:               testDevices(t),
		MaxAttemptsPerAddress: busyAttempts + 1,
		MaxAttemptsPerAccount: 3,
	})
	ctx := context.Background()

	slot, err := hasher.Hold(ctx)
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	for i := range busyAttempts {
		if _, err := service.Login(ctx, loginCmd("a guess")); !errors.Is(err, password.ErrBusy) {
			t.Fatalf("attempt %d with every slot held = %v, want password.ErrBusy", i, err)
		}
	}
	slot.Release()

	if _, err := service.Login(ctx, loginCmd(testPassword)); err != nil {
		t.Fatalf("the owner after %d busy refusals = %v, want a session", busyAttempts, err)
	}
	if _, err := service.Login(ctx, loginCmd(testPassword)); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("the attempt past the address budget = %v, want ErrTooManyAttempts: busy refusals did not spend it", err)
	}
}

func TestAnIPv6NetworkIsOneAddressToTheSignInThrottle(t *testing.T) {
	// A /64 is one subscriber (CLAUDE.md rule 9).
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	service := NewService(ServiceConfig{
		Users:                 userstest.New(),
		Sessions:              NewSessionStore(c, time.Hour),
		Audit:                 audit.New(&collectingSink{}),
		Limiter:               NewLimiter(c),
		Logger:                logging.New("error", io.Discard),
		Passwords:             passwordtest.NewHasher(),
		Devices:               testDevices(t),
		MaxAttemptsPerAddress: 1,
	})
	ctx := context.Background()

	_, _ = service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "2001:db8:1:2::1"})
	_, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "2001:db8:1:2::ffff"})

	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("a second host of the same /64 = %v, want ErrTooManyAttempts from the shared budget", err)
	}
}

type unlockFixture struct {
	service *Service
	sink    *collectingSink
	user    users.User
	repo    *userstest.Repository
}

func newUnlockFixture(t *testing.T, cfg ServiceConfig) *unlockFixture {
	t.Helper()
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	user := repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})
	sink := &collectingSink{}
	cfg.Users, cfg.Sessions, cfg.Audit = repo, NewSessionStore(c, time.Hour), audit.New(sink)
	cfg.Limiter, cfg.Logger = NewLimiter(c), logging.New("error", io.Discard)
	if cfg.Passwords == nil {
		cfg.Passwords = passwordtest.NewHasher()
	}
	if cfg.Devices == nil {
		cfg.Devices = testDevices(t)
	}
	return &unlockFixture{service: NewService(cfg), sink: sink, user: user, repo: repo}
}

func TestUnlockingSignInClearsTheAccountsGuessingLimitAndCeiling(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerAccount: maxLoginAttemptsPerAccountAddress + 1})
	ctx := context.Background()

	for range maxLoginAttemptsPerAccountAddress + 1 {
		_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.1"})
	}
	_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.2"})
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("before the unlock = %v, want ErrTooManyAttempts", err)
	}
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.3"}); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("before the unlock, another address = %v, want the ceiling's ErrTooManyAttempts", err)
	}

	if err := f.service.UnlockSignIn(ctx, uuid.New(), f.user.ID); err != nil {
		t.Fatalf("UnlockSignIn() returned error: %v", err)
	}

	if _, err := f.service.Login(ctx, LoginCommand{Login: "IVANOV", Password: testPassword, IP: "10.0.0.1"}); err != nil {
		t.Errorf("the owner at the locked address after the unlock = %v, want a session", err)
	}
}

func TestUnlockingSignInLeavesTheAddressBudgetAlone(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerAddress: 1})
	ctx := context.Background()
	_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.1"})

	if err := f.service.UnlockSignIn(ctx, uuid.New(), f.user.ID); err != nil {
		t.Fatalf("UnlockSignIn() returned error: %v", err)
	}

	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("after the unlock = %v, want the address budget still spent", err)
	}
}

func TestUnlockingSignInIsAuditedWithWhoDidIt(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{})
	actor := uuid.New()

	if err := f.service.UnlockSignIn(context.Background(), actor, f.user.ID); err != nil {
		t.Fatalf("UnlockSignIn() returned error: %v", err)
	}

	if len(f.sink.entries) != 1 {
		t.Fatalf("audit entries = %v, want one", f.sink.actions())
	}
	entry := f.sink.entries[0]
	if entry.Action != audit.ActionUserSignInUnlock || entry.ActorID == nil || *entry.ActorID != actor ||
		entry.Entity != "user" || entry.EntityID != f.user.ID.String() {
		t.Errorf("audit entry = %+v, want %s by the actor on the account", entry, audit.ActionUserSignInUnlock)
	}
}

func TestUnlockingSignInForAnUnknownAccountIsNotFound(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{})

	err := f.service.UnlockSignIn(context.Background(), uuid.New(), uuid.New())

	if !errors.Is(err, users.ErrNotFound) {
		t.Errorf("err = %v, want users.ErrNotFound", err)
	}
	if len(f.sink.entries) != 0 {
		t.Error("an unlock of nothing was audited")
	}
}

func (f *unlockFixture) trustedBrowser(t *testing.T, ip string) string {
	t.Helper()
	result, err := f.service.Login(context.Background(), LoginCommand{Login: "ivanov", Password: testPassword, IP: ip})
	if err != nil {
		t.Fatalf("the owner's first sign-in = %v, want a session", err)
	}
	if result.DeviceToken == "" {
		t.Fatal("a successful sign-in issued no device cookie")
	}
	return result.DeviceToken
}

func (f *unlockFixture) lockOut(t *testing.T, ip string) {
	t.Helper()
	ctx := context.Background()
	for range maxLoginAttemptsPerAccountAddress + 1 {
		_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: ip})
	}
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: ip}); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("without a device cookie after the rival's guesses = %v, want ErrTooManyAttempts", err)
	}
}

func TestTheOwnersBrowserSignsInThroughARivalsLockoutAtTheSameAddress(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerAddress: maxLoginAttemptsPerAccountAddress + 3})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	f.lockOut(t, "10.0.0.1")

	_, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	})
	if err != nil {
		t.Errorf("the owner's trusted browser = %v, want a session", err)
	}
}

func TestADeviceCookieForAnotherAccountDoesNotHelp(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{})
	f.repo.Add(users.User{
		Login: "petrov", FullName: "Pyotr Petrov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})
	result, err := f.service.Login(context.Background(), LoginCommand{Login: "petrov", Password: testPassword, IP: "10.0.0.1"})
	if err != nil {
		t.Fatalf("petrov's sign-in = %v", err)
	}
	f.lockOut(t, "10.0.0.1")

	_, err = f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: result.DeviceToken,
	})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("ivanov with petrov's device cookie = %v, want ErrTooManyAttempts", err)
	}
}

func TestAForgedOrExpiredDeviceCookieIsTreatedAsNone(t *testing.T) {
	expired, err := NewDeviceTrust(testDeviceSecret, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := newUnlockFixture(t, ServiceConfig{Devices: expired})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	f.lockOut(t, "10.0.0.1")

	payload, mac, _ := strings.Cut(cookie, ".")
	forged := payload[:len(payload)-1] + "A" + "." + mac
	if forged == cookie {
		forged = payload[:len(payload)-1] + "B" + "." + mac
	}
	if _, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: forged,
	}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("a forged device cookie = %v, want ErrTooManyAttempts", err)
	}

	expired.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if _, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("an expired device cookie = %v, want ErrTooManyAttempts", err)
	}
}

func TestAPasswordChangeRetiresEveryDeviceCookie(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	f.lockOut(t, "10.0.0.1")
	if _, err := f.repo.BumpSessionGeneration(context.Background(), f.user.ID); err != nil {
		t.Fatal(err)
	}

	_, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("a device cookie from before the password change = %v, want ErrTooManyAttempts", err)
	}
}

func TestABlockRetiresEveryDeviceCookie(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	f.lockOut(t, "10.0.0.1")
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{}); err != nil {
		t.Fatal(err)
	}

	_, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	})
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("a blocked account's device cookie = %v, want the ordinary limits' ErrTooManyAttempts", err)
	}
}

func TestGuessingThroughAStolenCookieIsBoundedByTheOrdinaryLimitsPastTheDevices(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 4})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()
	guess := LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.9.1", DeviceToken: cookie}

	for i := range 4 + maxLoginAttemptsPerAccountAddress {
		if _, err := f.service.Login(ctx, guess); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("guess %d through the cookie = %v, want ErrInvalidCredentials", i+1, err)
		}
	}
	if _, err := f.service.Login(ctx, LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.9.1", DeviceToken: cookie,
	}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("past the device limit and the thief's guessing limit = %v, want ErrTooManyAttempts", err)
	}
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); err != nil {
		t.Errorf("the owner without the cookie = %v, want a session", err)
	}
}

func TestUnlockingSignInClearsTheDeviceLimitToo(t *testing.T) {
	// With the address budget spent, only a cleared device limit lets the
	// owner's browser in.
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 2, MaxAttemptsPerAddress: 3})
	cookie := f.trustedBrowser(t, "10.0.0.9")
	ctx := context.Background()
	for range 3 {
		_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a rival's guess", IP: "10.0.0.1"})
	}
	for range 2 {
		_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a typo", IP: "10.0.0.1", DeviceToken: cookie})
	}
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("before the unlock = %v, want ErrTooManyAttempts", err)
	}

	if err := f.service.UnlockSignIn(ctx, uuid.New(), f.user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); err != nil {
		t.Errorf("the trusted browser after the unlock = %v, want a session", err)
	}
}

func (f *unlockFixture) tooManyAttemptRows() int {
	n := 0
	for _, e := range f.sink.entries {
		if e.Action == audit.ActionAuthLoginFailed && e.Payload["reason"] == ReasonTooManyAttempts {
			n++
		}
	}
	return n
}

func TestATrustedBrowserPastItsLimitFallsBackToTheOrdinaryPath(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerAddress: 2})
	cookie := f.trustedBrowser(t, "10.0.0.9")
	ctx := context.Background()

	for i := range DefaultMaxLoginAttemptsPerDevice + 2 {
		if _, err := f.service.Login(ctx, LoginCommand{
			Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
		}); err != nil {
			t.Fatalf("correct sign-in %d through the cookie = %v, want a session", i+1, err)
		}
	}

	// The 11th and 12th spent the address budget of two; the 13th has none.
	if _, err := f.service.Login(ctx, LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("past the device limit and the address budget = %v, want ErrTooManyAttempts", err)
	}
	if got := f.tooManyAttemptRows(); got != 1 {
		t.Errorf("the trail holds %d too_many_attempts rows, want 1: a spent trusted limit is not itself a refusal", got)
	}
}

func TestBusyRefusalsThroughATrustedBrowserCostTheOwnerNothing(t *testing.T) {
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 10 * time.Millisecond})
	f := newUnlockFixture(t, ServiceConfig{Passwords: hasher})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()

	slot, err := hasher.Hold(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxLoginAttemptsPerAccountAddress + 2 {
		if _, err := f.service.Login(ctx, LoginCommand{
			Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
		}); !errors.Is(err, password.ErrBusy) {
			t.Fatalf("attempt %d with every slot held = %v, want password.ErrBusy", i+1, err)
		}
	}
	slot.Release()

	if _, err := f.service.Login(ctx, LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	}); err != nil {
		t.Errorf("the owner after the busy refusals = %v, want a session", err)
	}
}

func TestPastTheTrustedBudgetALoopIsStillBounded(t *testing.T) {
	// A looping cookie gets its trusted attempts, then only what the
	// address budget or ceiling allow.
	loop := func(t *testing.T, f *unlockFixture, cookie string, ip func(int) string) int {
		t.Helper()
		signedIn := 0
		for i := range 30 {
			if _, err := f.service.Login(context.Background(), LoginCommand{
				Login: "ivanov", Password: testPassword, IP: ip(i), DeviceToken: cookie,
			}); err == nil {
				signedIn++
			}
		}
		return signedIn
	}

	t.Run("by the address budget", func(t *testing.T) {
		f := newUnlockFixture(t, ServiceConfig{
			MaxAttemptsPerDevice: 3, MaxTrustedAttemptsPerAccount: 3, MaxAttemptsPerAddress: 5,
		})
		cookie := f.trustedBrowser(t, "10.0.0.9")

		if got := loop(t, f, cookie, func(int) string { return "10.0.0.1" }); got != 3+5 {
			t.Errorf("%d sign-ins succeeded, want 3 trusted + 5 on the address budget", got)
		}
	})

	t.Run("by the account ceiling", func(t *testing.T) {
		f := newUnlockFixture(t, ServiceConfig{
			MaxAttemptsPerDevice: 3, MaxTrustedAttemptsPerAccount: 3, MaxAttemptsPerAccount: 4,
		})
		cookie := f.trustedBrowser(t, "10.0.0.9")

		// The issuing sign-in spent one of the ceiling's four; with fresh
		// addresses only the ceiling binds.
		if got := loop(t, f, cookie, func(i int) string { return fmt.Sprintf("10.0.3.%d", i) }); got != 3+3 {
			t.Errorf("%d sign-ins succeeded, want 3 trusted + the ceiling's remaining 3", got)
		}
	})
}

func TestFreshDeviceCookiesShareTheAccountsTrustedBudget(t *testing.T) {
	// Collected cookies share the trusted budget: past four, each attempt
	// pays the address budget of two.
	f := newUnlockFixture(t, ServiceConfig{
		MaxAttemptsPerDevice: 10, MaxTrustedAttemptsPerAccount: 4, MaxAttemptsPerAddress: 2,
	})
	ctx := context.Background()
	cookies := []string{
		f.trustedBrowser(t, "10.0.1.1"), f.trustedBrowser(t, "10.0.1.2"), f.trustedBrowser(t, "10.0.1.3"),
	}

	for i, cookie := range cookies {
		for j := range 2 {
			if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); err != nil {
				t.Fatalf("sign-in %d through cookie %d = %v, want a session", j+1, i+1, err)
			}
		}
	}

	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookies[2]}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("the seventh trusted sign-in = %v, want ErrTooManyAttempts: the fifth and sixth should have paid the address budget", err)
	}
}

func TestATrustedSignInRenewsTheCookieOnlyPastHalfItsLifetime(t *testing.T) {
	devices := testDevices(t)
	issued := time.Now()
	devices.now = func() time.Time { return issued }
	f := newUnlockFixture(t, ServiceConfig{Devices: devices})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()
	original, _ := devices.Verify(cookie, "ivanov")

	early, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie})
	if err != nil {
		t.Fatalf("trusted sign-in = %v", err)
	}
	if early.DeviceToken != "" {
		t.Error("a trusted sign-in early in the cookie's life renewed it")
	}

	devices.now = func() time.Time { return issued.Add(16 * 24 * time.Hour) }
	late, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie})
	if err != nil {
		t.Fatalf("trusted sign-in past half the lifetime = %v", err)
	}
	if late.DeviceToken == "" {
		t.Fatal("a trusted sign-in past half the cookie's lifetime did not renew it")
	}
	renewed, ok := devices.Verify(late.DeviceToken, "ivanov")
	if !ok || renewed.ID != original.ID {
		t.Errorf("renewed device = %v (ok %v), want the same device id %v", renewed.ID, ok, original.ID)
	}

	devices.now = func() time.Time { return issued.Add(31 * 24 * time.Hour) }
	if _, ok := devices.Verify(cookie, "ivanov"); ok {
		t.Error("the original cookie outlived its own lifetime")
	}
	if _, ok := devices.Verify(late.DeviceToken, "ivanov"); !ok {
		t.Error("the renewed cookie did not get a lifetime of its own")
	}
}

type slotWatch struct {
	hasher *password.Hasher
	held   []string
}

func (w *slotWatch) check(where string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	slot, err := w.hasher.Hold(ctx)
	if err != nil {
		w.held = append(w.held, where)
		return
	}
	slot.Release()
}

type watchedUsers struct {
	*userstest.Repository
	watch *slotWatch
}

func (u watchedUsers) ByLogin(ctx context.Context, login string) (users.User, error) {
	u.watch.check("ByLogin")
	return u.Repository.ByLogin(ctx, login)
}

type watchedSink struct {
	collectingSink
	watch *slotWatch
}

func (s *watchedSink) Append(ctx context.Context, e audit.Entry) error {
	s.watch.check("audit " + e.Action)
	return s.collectingSink.Append(ctx, e)
}

func TestTheHashingSlotIsNeverHeldAcrossTheLookupOrTheTrail(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: time.Second})
	watch := &slotWatch{hasher: hasher}
	sink := &watchedSink{watch: watch}
	service := NewService(ServiceConfig{
		Users:     watchedUsers{Repository: repo, watch: watch},
		Sessions:  NewSessionStore(c, time.Hour),
		Audit:     audit.New(sink),
		Limiter:   NewLimiter(c),
		Logger:    logging.New("error", io.Discard),
		Passwords: hasher,
		Devices:   testDevices(t),
	})
	ctx := context.Background()

	for range maxLoginAttemptsPerAccountAddress + 2 { // failures, then refusals
		_, _ = service.Login(ctx, loginCmd("a guess"))
	}
	_, _ = service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.2"})
	_, _ = service.Login(ctx, LoginCommand{Login: "nobody", Password: "whatever", IP: "10.0.0.3"})

	if len(watch.held) != 0 {
		t.Errorf("the hashing slot was held during: %v", watch.held)
	}
}

func TestOneAddressCannotFillTheQueueForAHashingSlot(t *testing.T) {
	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", Status: users.StatusActive,
		PasswordHash: passwordtest.Hash(t, testPassword),
	})

	// Roomy: each queued attempt runs a full verification, slow under -race.
	const wait = 20 * time.Second
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: wait})
	service := NewService(ServiceConfig{
		Users:     repo,
		Sessions:  NewSessionStore(c, time.Hour),
		Audit:     audit.New(&collectingSink{}),
		Limiter:   NewLimiter(c),
		Logger:    logging.New("error", io.Discard),
		Passwords: hasher,
		Devices:   testDevices(t),
	})
	ctx := context.Background()

	slot, err := hasher.Hold(ctx)
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}

	waiting := waitingPerSlot * hasher.Concurrency()
	done := make(chan error, waiting)
	for range waiting {
		go func() {
			_, err := service.Login(ctx, LoginCommand{Login: "nobody", Password: "a guess", IP: "10.0.0.1"})
			done <- err
		}()
	}
	time.Sleep(300 * time.Millisecond)

	started := time.Now()
	_, err = service.Login(ctx, LoginCommand{Login: "nobody", Password: "a guess", IP: "10.0.0.1"})
	elapsed := time.Since(started)
	if !errors.Is(err, password.ErrBusy) {
		t.Errorf("one attempt past the address's waiting share = %v, want password.ErrBusy", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the refusal took %v: it waited for a slot instead of being refused at once", elapsed)
	}

	other := make(chan error, 1)
	go func() {
		_, err := service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.2"})
		other <- err
	}()
	time.Sleep(100 * time.Millisecond)
	slot.Release()

	if err := <-other; err != nil {
		t.Errorf("a sign-in from another address = %v, want a session", err)
	}
	for range waiting {
		if err := <-done; !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("a waiting attempt = %v, want ErrInvalidCredentials once it got a slot", err)
		}
	}
}

func TestLoginEndsTheSessionTheBrowserAlreadyHad(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.service.Login(ctx, loginCmd(testPassword))
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	again := loginCmd(testPassword)
	again.PreviousToken = first.Token
	second, err := f.service.Login(ctx, again)
	if err != nil {
		t.Fatalf("Login() again = %v", err)
	}
	if _, err := f.service.Sessions().Get(ctx, first.Token); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("the replaced session still resolves: %v", err)
	}
	if _, err := f.service.Sessions().Get(ctx, second.Token); err != nil {
		t.Fatalf("the new session does not resolve: %v", err)
	}
}

func TestLoginKeepsThePreviousSessionUnlessItSucceeds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.service.Login(ctx, loginCmd(testPassword))
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}

	wrong := loginCmd("not the password")
	wrong.PreviousToken = first.Token
	if _, err := f.service.Login(ctx, wrong); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login(wrong) = %v, want ErrInvalidCredentials", err)
	}
	if _, err := f.service.Sessions().Get(ctx, first.Token); err != nil {
		t.Fatalf("a refused sign-in ended the session: %v", err)
	}

	garbled := loginCmd(testPassword)
	garbled.PreviousToken = "not-a-session"
	if _, err := f.service.Login(ctx, garbled); err != nil {
		t.Fatalf("Login() with a stale cookie = %v", err)
	}
}

func TestSigningInAgainIsNotAParallelSession(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	trail := cache.NewMemory(100)
	t.Cleanup(func() { _ = trail.Close() })
	events := &countingEvents{}
	tracker := monitor.NewTracker(trail, events, f.service.Sessions(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	visit := monitor.Visit{Contest: uuid.New(), Registration: uuid.New(), Address: netip.MustParseAddr("10.0.0.1")}

	first, err := f.service.Login(ctx, loginCmd(testPassword))
	if err != nil {
		t.Fatalf("Login() = %v", err)
	}
	visit.Session = monitor.SessionTag(first.Token)
	tracker.Observe(ctx, visit)

	again := loginCmd(testPassword)
	again.PreviousToken = first.Token
	second, err := f.service.Login(ctx, again)
	if err != nil {
		t.Fatalf("Login() again = %v", err)
	}
	visit.Session = monitor.SessionTag(second.Token)
	tracker.Observe(ctx, visit)

	if events.count != 0 {
		t.Fatalf("signing in again wrote %d events, want none", events.count)
	}
}
