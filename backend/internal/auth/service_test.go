package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
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

func (s *collectingSink) AppendMany(_ context.Context, entries []audit.Entry) error {
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
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

	result, err := f.service.Login(context.Background(), loginCmd(testPassword))

	if !errors.Is(err, ErrAccountBlocked) {
		t.Errorf("err = %v, want ErrAccountBlocked", err)
	}
	if result.Token != "" {
		t.Error("a blocked account received a session")
	}
}

// TestDeletedAccountIsRejectedEvenWithTheRightPassword is the central claim
// the whole feature rests on: deletion is a status, not a column, and sign-in
// already refuses anything whose status is not active. The account's password
// is still correct and its row still exists — the refusal has to come from
// the status alone.
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
	// The account owner deserves to be told they are blocked, but only after
	// proving they own it. A wrong guess must not reveal that the login exists.
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
	// An administrator-issued password has to end in the user choosing theirs.
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
	// Failed attempts are the signal that matters when investigating later.
	f := newFixture(t)

	_, _ = f.service.Login(context.Background(), loginCmd("wrong password"))

	if got := f.sink.actions(); len(got) != 1 || got[0] != audit.ActionAuthLoginFailed {
		t.Errorf("audit actions = %v, want one %q", got, audit.ActionAuthLoginFailed)
	}
	// The record has to say why, or an administrator reading it cannot tell a
	// mistyped password from a blocked account from a sweep of guesses — three
	// different conversations to have.
	if reason := f.sink.entries[0].Payload["reason"]; reason != ReasonInvalidCredentials {
		t.Errorf("reason = %v, want %q", reason, ReasonInvalidCredentials)
	}
}

// TestBlockedAccountFailureRecordsWhyItIsBlocked covers the case where the
// caller does own the account: the password matched, and only then did the
// block refuse the sign-in. The endpoint tells this caller the account is
// blocked (they proved ownership), so the trail recording the same fact adds
// no distinction beyond what the wire already gave away.
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

// TestBlockedAccountWithWrongPasswordNeverRecordsTheBlock is the trail-side
// half of TestBlockedAccountLooksLikeAnyOtherFailureToSomeoneGuessing: a
// wrong guess against a blocked account must record exactly what a wrong
// guess against any other account records. Recording ReasonAccountBlocked
// here would make the audit trail an oracle the endpoint itself was built to
// deny — an administrator (or anyone who later gets read access to the same
// row) would learn the account exists and is blocked from a password that
// never matched anything.
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

// TestThrottledLoginRecordsTooManyAttempts covers both throttle windows: the
// caller is told "too many attempts" either way, and the trail says no more
// than that either.
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
	// Otherwise an attacker who eventually guesses right walks straight in,
	// which is exactly the case throttling exists for.
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

	// The counter is clear, so a fresh run of failures is allowed again.
	for range maxLoginAttemptsPerAccountAddress {
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

	// One address counter, plus the two account counters — this account from
	// this address, and the account across all addresses — for each of the
	// two attempts the address was allowed. The eight refused attempts left
	// nothing behind.
	if got := c.Len(); got != 5 {
		t.Errorf("the cache holds %d counters, want 5: refused attempts created account keys", got)
	}
}

func TestAnOverlongLoginNeverBecomesARateLimitKey(t *testing.T) {
	// accountSubject turns the login into a rate-limit cache key verbatim. No
	// real account's login can exceed users.MaxLoginLength, so a longer one
	// must be refused before it can mint a counter of its own size — an
	// unauthenticated caller could otherwise fill the in-process cache with
	// megabyte-sized keys, one request at a time. The address counter above
	// it in checkThrottle still gets spent, which is what c.Len() == 1 (and
	// not 0) below proves.
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

func TestASignInThatCannotGetAHashingSlotIsRefusedAndStillCounted(t *testing.T) {
	// Every verification holds 64 MiB, so the hasher admits a bounded number
	// at once. An attempt that finds every slot taken is refused within the
	// wait rather than queued — and it has already spent its address budget:
	// a refusal that cost the caller nothing could be retried at no cost.
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

	// The slot is free again, and the address budget of one is already spent
	// by the refused attempt.
	if _, err := service.Login(context.Background(), loginCmd(testPassword)); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("the next attempt = %v, want ErrTooManyAttempts: the refusal was not counted", err)
	}
}

func TestAThrottledAttemptNeverStoresAnOversizedLogin(t *testing.T) {
	// A caller whose address budget is spent is still recorded, and the
	// login is what the record carries. The request body is bounded at a
	// megabyte, not the field: without a bound of its own, each refused
	// attempt could write a megabyte into a trail kept for a year.
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
	// No stored digest can be of a password longer than password.MaxLength,
	// so the answer is known at once. It is the ordinary "wrong login or
	// password", so nothing is learned about the account, and it comes after
	// the address counter, so it is not a way around the address budget.
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

// throttleService builds a service over a real account with the given
// account-wide ceiling.
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
	// Logins are not secret — a public leaderboard may list them — so a
	// counter keyed on the login alone let anybody who knew one keep its
	// owner from signing in. The guessing limit is per account and address:
	// it still stops the guesser, and the owner elsewhere is not their
	// counter.
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
	// Keying the guessing limit on the address hands a guesser with many
	// addresses a fresh budget at each. The account-wide ceiling is the
	// backstop: far above what one person mistyping reaches, and still a
	// limit on a distributed guess.
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
	// The per-address guessing limit is checked first, and a refusal there
	// stops before the account-wide counter. Otherwise one address repeating
	// refused attempts would climb to the ceiling on its own and lock the
	// owner out everywhere — the lockout the address key exists to prevent.
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
	// A refusal for load says nothing about the password, so it must not
	// count towards the account's guessing limit or its ceiling: otherwise a
	// flood that fills the hashing slots locks out whoever it names. It still
	// spends the address budget, so it is not free to repeat either.
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
	// A /64 is one subscriber. Taken host by host, each of its addresses
	// would be a fresh address budget and a fresh guessing limit per account.
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

// unlockFixture is a service over one real account, with the sink and the
// account exposed for the unlock tests.
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
	cfg.Limiter, cfg.Logger, cfg.Passwords = NewLimiter(c), logging.New("error", io.Discard), passwordtest.NewHasher()
	if cfg.Devices == nil {
		cfg.Devices = testDevices(t)
	}
	return &unlockFixture{service: NewService(cfg), sink: sink, user: user, repo: repo}
}

func TestUnlockingSignInClearsTheAccountsGuessingLimitAndCeiling(t *testing.T) {
	// A rival sharing the owner's address, or a guess spread across many
	// addresses, can still shut an account for a window. Staff can reopen it
	// at once rather than tell a participant to wait out the clock.
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
	// The address budget is about a machine, not an account: clearing it
	// for one account's sake would reopen a sweep across every other login.
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

// trustedBrowser signs in successfully from ip and returns the device cookie
// that sign-in issued.
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

// lockOut spends ivanov's guessing limit, and the address budget, from ip.
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
	// A lecture hall is one address. A rival there can spend the owner's
	// guessing limit, and the address budget with it, but not the limit of
	// a browser the owner has already signed in from.
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
	// Changing a password is what somebody does when they think another
	// person has the account; that person's browser must not keep its trust.
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

func TestTheDeviceLimitStopsGuessingThroughAStolenCookie(t *testing.T) {
	// A copied cookie skips the address and account limits, so it carries a
	// guessing limit of its own, keyed on the device it names.
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 4})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()

	for i := range 4 {
		if _, err := f.service.Login(ctx, LoginCommand{
			Login: "ivanov", Password: "a guess", IP: fmt.Sprintf("10.0.9.%d", i), DeviceToken: cookie,
		}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("guess %d through the cookie = %v, want ErrInvalidCredentials", i, err)
		}
	}
	if _, err := f.service.Login(ctx, LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.9.99", DeviceToken: cookie,
	}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("past the device limit = %v, want ErrTooManyAttempts", err)
	}
	// The owner's own guessing limit at their own address was not spent.
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1"}); err != nil {
		t.Errorf("the owner without the cookie = %v, want a session", err)
	}
}

func TestUnlockingSignInClearsTheDeviceLimitToo(t *testing.T) {
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 2})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()
	for range 3 {
		_, _ = f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: "a guess", IP: "10.0.0.1", DeviceToken: cookie})
	}

	if err := f.service.UnlockSignIn(ctx, uuid.New(), f.user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); err != nil {
		t.Errorf("the trusted browser after the unlock = %v, want a session", err)
	}
}

func TestTrustedSignInsAreBoundedEvenWithTheRightPassword(t *testing.T) {
	// A success does not give a device its attempts back: otherwise the
	// account's own cookie signs in without limit, and every one of those
	// sign-ins takes a hashing slot, a session and an audit row.
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 5, MaxTrustedAttemptsPerAccount: 50})
	cookie := f.trustedBrowser(t, "10.0.0.1")
	ctx := context.Background()

	for i := range 5 {
		if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); err != nil {
			t.Fatalf("trusted sign-in %d = %v, want a session within the device limit", i, err)
		}
	}
	if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("the trusted sign-in past the device limit = %v, want ErrTooManyAttempts", err)
	}
}

func TestFreshDeviceCookiesShareTheAccountsTrustedBudget(t *testing.T) {
	// Signing in again mints another device id, so a per-device limit alone
	// multiplies by however many cookies were collected in advance. Every
	// trusted attempt at the account counts once more, across all of them.
	f := newUnlockFixture(t, ServiceConfig{MaxAttemptsPerDevice: 10, MaxTrustedAttemptsPerAccount: 4})
	ctx := context.Background()
	cookies := []string{
		f.trustedBrowser(t, "10.0.1.1"), f.trustedBrowser(t, "10.0.1.2"), f.trustedBrowser(t, "10.0.1.3"),
	}

	signedIn := 0
	for _, cookie := range cookies {
		for range 2 {
			if _, err := f.service.Login(ctx, LoginCommand{Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie}); err == nil {
				signedIn++
			} else if !errors.Is(err, ErrTooManyAttempts) {
				t.Fatalf("trusted sign-in = %v, want a session or ErrTooManyAttempts", err)
			}
		}
	}

	if signedIn != 4 {
		t.Errorf("%d trusted sign-ins succeeded across three cookies, want the account budget of 4", signedIn)
	}
}

func TestATrustedSignInDoesNotRenewTheDeviceCookie(t *testing.T) {
	// A cookie renewed by every trusted sign-in would never expire for a
	// browser that keeps using it, stolen or not. It lives out the lifetime it
	// was issued with; the next ordinary sign-in issues the next one.
	f := newUnlockFixture(t, ServiceConfig{})
	cookie := f.trustedBrowser(t, "10.0.0.1")

	result, err := f.service.Login(context.Background(), LoginCommand{
		Login: "ivanov", Password: testPassword, IP: "10.0.0.1", DeviceToken: cookie,
	})
	if err != nil {
		t.Fatalf("trusted sign-in = %v", err)
	}
	if result.DeviceToken != "" {
		t.Error("a trusted sign-in issued a new device cookie, extending the old one's lifetime")
	}
}

// slotWatch reports whether the hasher's only slot is free at the moment it is
// asked, recording every time it was not.
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
	// A slot is 64 MiB and one of a handful. Held while the database answers a
	// lookup or takes an audit row, it stands idle while sign-ins queue for it.
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
