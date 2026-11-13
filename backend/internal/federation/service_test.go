package federation_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

const issuer = "https://login.example.edu/tenant/v2.0"

var now = time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)

type discardSink struct{}

func (discardSink) Append(context.Context, audit.Entry) error       { return nil }
func (discardSink) AppendMany(context.Context, []audit.Entry) error { return nil }

// pendingLogins stands in for the sign-ups waiting for their code.
type pendingLogins map[string]bool

func (p pendingLogins) LoginHeld(_ context.Context, login, _ string, _ time.Time) (bool, error) {
	return p[strings.ToLower(login)], nil
}

// countingStore records every lookup federation makes.
type countingStore struct {
	federation.Store
	calls, byEmail int
}

func (s *countingStore) ByExternalIdentity(ctx context.Context, provider, issuer, subject string) (users.User, error) {
	s.calls++
	return s.Store.ByExternalIdentity(ctx, provider, issuer, subject)
}

func (s *countingStore) ByEmail(ctx context.Context, email string) (users.User, error) {
	s.calls++
	s.byEmail++
	return s.Store.ByEmail(ctx, email)
}

// hookedAccounts runs before once, just ahead of the first write, as a
// rival request committing in between.
type hookedAccounts struct {
	federation.Accounts
	before func()
	linked []users.LinkExternalCommand
}

func (a *hookedAccounts) fire() {
	if a.before != nil {
		hook := a.before
		a.before = nil
		hook()
	}
}

func (a *hookedAccounts) CreateExternal(ctx context.Context, cmd users.CreateExternalCommand) (users.User, error) {
	a.fire()
	return a.Accounts.CreateExternal(ctx, cmd)
}

func (a *hookedAccounts) LinkExternal(ctx context.Context, cmd users.LinkExternalCommand) (users.User, error) {
	a.fire()
	a.linked = append(a.linked, cmd)
	return a.Accounts.LinkExternal(ctx, cmd)
}

type fixture struct {
	repo     *userstest.Repository
	store    *countingStore
	accounts *hookedAccounts
	pending  pendingLogins
	policy   federation.Policy
	random   []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	repo.GrantRole("organizer", "contests.manage")
	service := users.NewService(repo, audit.New(discardSink{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()).
		WithExternalIdentities(repo)
	return &fixture{
		repo:     repo,
		store:    &countingStore{Store: repo},
		accounts: &hookedAccounts{Accounts: service},
		pending:  pendingLogins{},
		policy:   federation.Policy{CreateAccounts: true},
	}
}

func (f *fixture) service() *federation.Service {
	return federation.NewService(federation.Config{
		Policy: f.policy, Store: f.store, Accounts: f.accounts, PendingLogins: f.pending,
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(f.random),
	})
}

func (f *fixture) link(t *testing.T, u users.User, subject string) {
	t.Helper()
	identity := users.ExternalIdentity{UserID: u.ID, Provider: users.ProviderMicrosoft, Issuer: issuer, Subject: subject}
	if err := f.repo.LinkExternalIdentity(t.Context(), identity); err != nil {
		t.Fatalf("link %s: %v", subject, err)
	}
}

// accountCount counts every account in the repository, under any login.
func (f *fixture) accountCount() int {
	return f.repo.Len()
}

func claims(subject string) federation.Claims {
	return federation.Claims{
		Provider: users.ProviderMicrosoft, Issuer: issuer, Subject: subject,
		Email: "ana@example.edu", Name: "Ana Popescu", LoginHint: "ana@example.edu",
	}
}

func trusted(subject string) federation.Claims {
	c := claims(subject)
	c.EmailTrusted = true
	return c
}

func TestAKnownIdentitySignsIntoItsAccount(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Roles: []string{users.RoleStudent}})
	f.link(t, ana, "sub-ana")

	got, err := f.service().Resolve(t.Context(), claims("sub-ana"))

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.Equal(now) {
		t.Errorf("identity last used %v, want %v", used, now)
	}
	if f.store.byEmail != 0 {
		t.Errorf("looked up by email %d times, want none for a known identity", f.store.byEmail)
	}
}

func TestATrustedEmailLinksToTheExistingStudent(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	c := trusted("sub-ana")
	c.Email = "  Ana@Example.EDU "

	got, err := f.service().Resolve(t.Context(), c)

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 1 || linked[0].Subject != "sub-ana" || linked[0].Email != "ana@example.edu" {
		t.Errorf("identities = %+v, want sub-ana with the address lowercased", linked)
	}
	stored, _ := f.repo.Get(ana.ID)
	if stored.EmailVerifiedAt == nil || !stored.EmailVerifiedAt.Equal(now) {
		t.Errorf("email verified at %v, want %v", stored.EmailVerifiedAt, now)
	}
	if got.EmailVerifiedAt == nil || !got.EmailVerifiedAt.Equal(now) {
		t.Errorf("returned account verified at %v, want %v", got.EmailVerifiedAt, now)
	}
}

// oneTimeStudent is a staff-made student still on its issued one-time
// password, which the person signing in through the provider never had.
func (f *fixture) oneTimeStudent(t *testing.T) users.User {
	t.Helper()
	return f.repo.Add(users.User{
		Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent},
		PasswordHash: "issued-one-time-password-hash", MustChangePassword: true, SessionGeneration: 3,
	})
}

func assertEnteredWithoutTheOneTimePassword(t *testing.T, f *fixture, ana, got users.User) {
	t.Helper()
	stored, _ := f.repo.Get(ana.ID)
	if stored.HasPassword() || stored.MustChangePassword {
		t.Errorf("stored hash %q, must change %v: want the one-time password dropped", stored.PasswordHash, stored.MustChangePassword)
	}
	// The session opens at the returned generation, so it must be the one
	// stored, or it is dead on arrival.
	if got.ID != ana.ID || got.MustChangePassword || got.SessionGeneration != stored.SessionGeneration {
		t.Errorf("returned %+v, want ana's account as stored after the change (generation %d)", got, stored.SessionGeneration)
	}
}

// Once an identity is linked, a one-time password on the account was issued
// by an administrator on purpose, after the link: the person holds it, and
// the sign-in sends them on to change it.
func TestAKnownIdentityKeepsAOneTimePasswordIssuedAfterTheLink(t *testing.T) {
	f := newFixture(t)
	ana := f.oneTimeStudent(t)
	f.link(t, ana, "sub-ana")

	got, err := f.service().Resolve(t.Context(), claims("sub-ana"))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	stored, _ := f.repo.Get(ana.ID)
	if stored.PasswordHash != ana.PasswordHash || !stored.MustChangePassword || stored.SessionGeneration != ana.SessionGeneration {
		t.Errorf("stored %+v, want the issued password, its flag and the sessions kept", stored)
	}
	if got.ID != ana.ID || !got.MustChangePassword {
		t.Errorf("returned %+v, want ana's account still bound to change its password", got)
	}
}

func TestATrustedEmailLinksAnAccountWithoutItsOneTimePassword(t *testing.T) {
	f := newFixture(t)
	ana := f.oneTimeStudent(t)

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	assertEnteredWithoutTheOneTimePassword(t, f, ana, got)
	if got.EmailVerifiedAt == nil || !got.EmailVerifiedAt.Equal(now) {
		t.Errorf("returned account verified at %v, want %v", got.EmailVerifiedAt, now)
	}
}

func TestATrustedEmailWithNoAccountCreatesAVerifiedOne(t *testing.T) {
	f := newFixture(t)

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	stored, _ := f.repo.Get(got.ID)
	if stored.Login != "ana" || stored.FullName != "Ana Popescu" || stored.HasPassword() || stored.IsStaff() {
		t.Errorf("created %+v, want student ana without a password", stored)
	}
	if stored.Email != "ana@example.edu" || stored.EmailVerifiedAt == nil || !stored.EmailVerifiedAt.Equal(now) {
		t.Errorf("email %q verified at %v, want ana@example.edu at %v", stored.Email, stored.EmailVerifiedAt, now)
	}
	if linked := f.repo.Identities(got.ID); len(linked) != 1 || linked[0].Subject != "sub-ana" {
		t.Errorf("identities = %+v, want sub-ana", linked)
	}
}

func TestAnUntrustedEmailGetsAnAccountWithoutEmail(t *testing.T) {
	f := newFixture(t)
	owner := f.repo.Add(users.User{Login: "owner", FullName: "Owner", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})

	got, err := f.service().Resolve(t.Context(), claims("sub-ana"))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	if got.ID == owner.ID || len(f.repo.Identities(owner.ID)) != 0 {
		t.Fatal("an untrusted email reached the account that holds the address")
	}
	if f.store.byEmail != 0 {
		t.Errorf("looked up by email %d times, want none for an untrusted address", f.store.byEmail)
	}
	stored, _ := f.repo.Get(got.ID)
	if stored.Email != "" || stored.EmailVerifiedAt != nil {
		t.Errorf("new account's email %q verified at %v, want none", stored.Email, stored.EmailVerifiedAt)
	}
	if linked := f.repo.Identities(got.ID); len(linked) != 1 || linked[0].Email != "ana@example.edu" {
		t.Errorf("identities = %+v, want the claimed address kept on the identity only", linked)
	}
}

func TestNeverLinksAStaffAccount(t *testing.T) {
	f := newFixture(t)
	staff := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Email: "ana@example.edu", Roles: []string{"organizer"}})

	_, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if !errors.Is(err, federation.ErrStaffAccount) {
		t.Fatalf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if len(f.repo.Identities(staff.ID)) != 0 {
		t.Error("the staff account was linked")
	}
	if n := f.accountCount(); n != 1 {
		t.Errorf("accounts = %d, want only the staff one", n)
	}
}

// A contest's manager reads its reference answers with no global role.
func TestNeverLinksAContestManager(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.repo.AppointToContest(ana.ID)

	_, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if !errors.Is(err, federation.ErrStaffAccount) {
		t.Fatalf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if len(f.repo.Identities(ana.ID)) != 0 {
		t.Error("the contest manager's account was linked")
	}
}

func TestAKnownIdentityOfAnAccountMadeAContestManagerIsRefused(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Roles: []string{users.RoleStudent}})
	f.link(t, ana, "sub-ana")
	f.repo.AppointToContest(ana.ID)

	if _, err := f.service().Resolve(t.Context(), claims("sub-ana")); !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
}

// Call 9 holds over time: an account promoted after its identity was linked
// is no longer entered through it.
func TestAKnownIdentityOfAnAccountMadeStaffIsRefused(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Roles: []string{"organizer"}})
	f.link(t, ana, "sub-ana")

	if _, err := f.service().Resolve(t.Context(), claims("sub-ana")); !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.IsZero() {
		t.Errorf("a refused sign-in stamped the identity at %v", used)
	}
}

func TestCannotEnterABlockedAccount(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		want   error
	}{
		"blocked": {users.StatusBlocked, users.ErrAccountBlocked},
		"deleted": {users.StatusDeleted, users.ErrAccountDeleted},
	} {
		t.Run(name+", known identity", func(t *testing.T) {
			f := newFixture(t)
			ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Status: tc.status, Roles: []string{users.RoleStudent}})
			f.link(t, ana, "sub-ana")

			if _, err := f.service().Resolve(t.Context(), claims("sub-ana")); !errors.Is(err, tc.want) {
				t.Errorf("Resolve() = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("blocked, trusted email", func(t *testing.T) {
		f := newFixture(t)
		ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Status: users.StatusBlocked, Roles: []string{users.RoleStudent}})

		if _, err := f.service().Resolve(t.Context(), trusted("sub-ana")); !errors.Is(err, users.ErrAccountBlocked) {
			t.Errorf("Resolve() = %v, want ErrAccountBlocked", err)
		}
		if len(f.repo.Identities(ana.ID)) != 0 {
			t.Error("the blocked account was linked")
		}
		if n := f.accountCount(); n != 1 {
			t.Errorf("accounts = %d, want only the blocked one", n)
		}
	})
}

func TestNoAccountIsCreatedWhileSignupIsClosed(t *testing.T) {
	for name, c := range map[string]federation.Claims{
		"untrusted email":         claims("sub-new"),
		"trusted, no account":     trusted("sub-new"),
		"trusted, someone else's": func() federation.Claims { c := trusted("sub-new"); c.Email = "bob@example.edu"; return c }(),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.policy.CreateAccounts = false

			if _, err := f.service().Resolve(t.Context(), c); !errors.Is(err, federation.ErrSignupClosed) {
				t.Errorf("Resolve() = %v, want ErrSignupClosed", err)
			}
			if n := f.accountCount(); n != 0 {
				t.Errorf("accounts created = %d, want none", n)
			}
		})
	}

	t.Run("known identity and trusted link still enter", func(t *testing.T) {
		f := newFixture(t)
		f.policy.CreateAccounts = false
		known := f.repo.Add(users.User{Login: "known", FullName: "Known", Roles: []string{users.RoleStudent}})
		f.link(t, known, "sub-known")
		ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})

		if got, err := f.service().Resolve(t.Context(), claims("sub-known")); err != nil || got.ID != known.ID {
			t.Errorf("known identity: Resolve() = %v, %v; want its account", got.ID, err)
		}
		if got, err := f.service().Resolve(t.Context(), trusted("sub-ana")); err != nil || got.ID != ana.ID {
			t.Errorf("trusted email: Resolve() = %v, %v; want ana's account", got.ID, err)
		}
	})
}

func TestAGeneratedLoginIsUniqueAndBounded(t *testing.T) {
	long := strings.Repeat("a", 120)
	for name, tc := range map[string]struct {
		hint, name string
		want       string
	}{
		"the hint's local part":        {"Ana.Popescu@example.edu", "Ana Popescu", "ana.popescu"},
		"a hint without an at sign":    {"Ana_P", "", "ana_p"},
		"only plain characters":        {"ștefan+ana@example.edu", "", "tefanana"},
		"the name when the hint is ''": {"", "Ana-Maria Popescu", "ana-mariapopescu"},
		"the name when the hint has ∅": {"Анна@example.edu", "Ana", "ana"},
		"no leading or trailing marks": {"..ana_@example.edu", "", "ana"},
		"user when nothing is left":    {"Анна@example.edu", "Анна Попеску", "user"},
		"cut to 90 characters":         {long + "@example.edu", "", strings.Repeat("a", 90)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c := claims("sub-ana")
			c.LoginHint, c.Name = tc.hint, tc.name

			got, err := f.service().Resolve(t.Context(), c)
			if err != nil {
				t.Fatalf("Resolve() = %v", err)
			}
			if got.Login != tc.want {
				t.Errorf("login = %q, want %q", got.Login, tc.want)
			}
			if got.FullName == "" {
				t.Error("the account has no name")
			}
		})
	}

	t.Run("taken by an account or a pending sign-up", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Add(users.User{Login: "Ana", FullName: "Ana"})
		f.pending["ana-2"] = true

		got, err := f.service().Resolve(t.Context(), claims("sub-ana"))
		if err != nil || got.Login != "ana-3" {
			t.Errorf("Resolve() = %q, %v; want ana-3", got.Login, err)
		}
	})

	t.Run("a random suffix after fifty tries", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Add(users.User{Login: "ana", FullName: "Ana"})
		for i := 2; i <= 50; i++ {
			f.repo.Add(users.User{Login: fmt.Sprintf("ana-%d", i), FullName: "Ana"})
		}
		f.random = []byte{0xab, 0xcd, 0xef}

		got, err := f.service().Resolve(t.Context(), claims("sub-ana"))
		if err != nil || got.Login != "ana-abcdef" {
			t.Errorf("Resolve() = %q, %v; want ana-abcdef", got.Login, err)
		}
	})

	t.Run("within the login bound with a suffix", func(t *testing.T) {
		f := newFixture(t)
		c := claims("sub-ana")
		c.LoginHint = long + "@example.edu"
		f.repo.Add(users.User{Login: strings.Repeat("a", 90), FullName: "Ana"})

		got, err := f.service().Resolve(t.Context(), c)
		if err != nil || got.Login != strings.Repeat("a", 90)+"-2" || len(got.Login) > users.MaxLoginLength {
			t.Errorf("Resolve() = %q, %v; want ninety a's and -2", got.Login, err)
		}
		if !regexp.MustCompile(`^[a-z0-9._-]+$`).MatchString(got.Login) {
			t.Errorf("login %q holds more than plain characters", got.Login)
		}
	})
}

func TestOversizedClaimsAreRefused(t *testing.T) {
	over := func(n int) string { return strings.Repeat("x", n+1) }
	for name, edit := range map[string]func(*federation.Claims){
		"long provider":    func(c *federation.Claims) { c.Provider = over(federation.MaxProviderLength) },
		"long issuer":      func(c *federation.Claims) { c.Issuer = over(federation.MaxIssuerLength) },
		"long subject":     func(c *federation.Claims) { c.Subject = over(federation.MaxSubjectLength) },
		"long email":       func(c *federation.Claims) { c.Email = over(federation.MaxEmailLength-12) + "@example.edu" },
		"long name":        func(c *federation.Claims) { c.Name = over(federation.MaxNameLength) },
		"long login hint":  func(c *federation.Claims) { c.LoginHint = over(federation.MaxLoginHintLength) },
		"no subject":       func(c *federation.Claims) { c.Subject = "" },
		"no issuer":        func(c *federation.Claims) { c.Issuer = "" },
		"unknown provider": func(c *federation.Claims) { c.Provider = "elsewhere" },
		"not an email":     func(c *federation.Claims) { c.Email = "Ana <ana@example.edu>" },
		"invalid UTF-8":    func(c *federation.Claims) { c.Name = "Ana \xff" },
		"a control char":   func(c *federation.Claims) { c.Subject = "sub\x00ana" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c := trusted("sub-ana")
			edit(&c)

			if _, err := f.service().Resolve(t.Context(), c); !errors.Is(err, federation.ErrInvalidClaims) {
				t.Errorf("Resolve() = %v, want ErrInvalidClaims", err)
			}
			if f.store.calls != 0 {
				t.Errorf("store calls = %d, want none before the claims are valid", f.store.calls)
			}
		})
	}
}

// An address the provider does not vouch for is never used, so one it gets
// wrong must not stop a person already linked, nor a new account.
func TestAnUntrustedInvalidEmailIsDropped(t *testing.T) {
	for name, email := range map[string]string{
		"too long":       strings.Repeat("x", federation.MaxEmailLength) + "@example.edu",
		"not an address": "Ana <ana@example.edu>",
		"no at sign":     "ana.example.edu",
		"a control char": "ana\x00@example.edu",
		"invalid UTF-8":  "ana\xff@example.edu",
	} {
		t.Run(name+", linked", func(t *testing.T) {
			f := newFixture(t)
			ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Roles: []string{users.RoleStudent}})
			f.link(t, ana, "sub-ana")
			c := claims("sub-ana")
			c.Email = email

			if got, err := f.service().Resolve(t.Context(), c); err != nil || got.ID != ana.ID {
				t.Errorf("Resolve() = %v, %v; want ana's account", got.ID, err)
			}
		})
		t.Run(name+", new", func(t *testing.T) {
			f := newFixture(t)
			c := claims("sub-ion")
			c.Email = email

			got, err := f.service().Resolve(t.Context(), c)
			if err != nil {
				t.Fatalf("Resolve() = %v, want a new account", err)
			}
			if stored, _ := f.repo.Get(got.ID); stored.Email != "" {
				t.Errorf("new account's email = %q, want none", stored.Email)
			}
			if linked := f.repo.Identities(got.ID); len(linked) != 1 || linked[0].Email != "" {
				t.Errorf("identities = %+v, want one without the invalid address", linked)
			}
		})
	}
}

func TestATrustedInvalidEmailIsRefused(t *testing.T) {
	for name, email := range map[string]string{
		"too long":       strings.Repeat("x", federation.MaxEmailLength) + "@example.edu",
		"not an address": "Ana <ana@example.edu>",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			c := trusted("sub-ana")
			c.Email = email

			if _, err := f.service().Resolve(t.Context(), c); !errors.Is(err, federation.ErrInvalidClaims) {
				t.Errorf("Resolve() = %v, want ErrInvalidClaims", err)
			}
		})
	}
}

// Two first sign-ins of one person race: the rival commits its account and
// link first, and this one enters the rival's account instead of making a
// second.
func TestTwoFirstSignInsOfOneIdentityEnterOneAccount(t *testing.T) {
	f := newFixture(t)
	var rival users.User
	f.accounts.before = func() {
		var err error
		rival, err = f.service().Resolve(t.Context(), trusted("sub-ana"))
		if err != nil {
			t.Fatalf("rival Resolve() = %v", err)
		}
	}

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if err != nil || got.ID != rival.ID {
		t.Fatalf("Resolve() = %v, %v; want the rival's account %v", got.ID, err, rival.ID)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.Equal(now) {
		t.Errorf("identity last used %v, want the lost race entered as a known identity", used)
	}
}

func TestALinkThatLosesARaceEntersTheWinner(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.accounts.before = func() { f.link(t, ana, "sub-ana") }

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.Equal(now) {
		t.Errorf("identity last used %v, want %v", used, now)
	}
}

func TestAnAccountMadeStaffBeforeTheLinkIsRefused(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.accounts.before = func() {
		if err := f.repo.ReplaceRoles(t.Context(), ana.ID, []string{"organizer"}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 0 {
		t.Errorf("identities = %+v, want none", linked)
	}
}

func homeDirectory(subject string) federation.Claims {
	c := trusted(subject)
	c.HomeDirectory = true
	return c
}

func TestAHomeDirectoryMemberLinksAStaffAccount(t *testing.T) {
	f := newFixture(t)
	org := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Email: "ana@example.edu", Roles: []string{"organizer"}})

	got, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana"))

	if err != nil || got.ID != org.ID {
		t.Fatalf("Resolve() = %v, %v; want the organizer's account", got.ID, err)
	}
	if linked := f.repo.Identities(org.ID); len(linked) != 1 || linked[0].Subject != "sub-ana" {
		t.Errorf("identities = %+v, want sub-ana", linked)
	}
	if n := f.accountCount(); n != 1 {
		t.Errorf("accounts = %d, want only the organizer's", n)
	}
}

func TestAHomeDirectoryMemberLinksAContestManager(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.repo.AppointToContest(ana.ID)

	got, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana"))

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 1 {
		t.Errorf("identities = %+v, want one", linked)
	}
}

func TestAHomeDirectoryMemberEntersALinkedStaffAccount(t *testing.T) {
	f := newFixture(t)
	org := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Roles: []string{"organizer"}})
	f.link(t, org, "sub-ana")

	got, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana"))

	if err != nil || got.ID != org.ID {
		t.Fatalf("Resolve() = %v, %v; want the organizer's account", got.ID, err)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.Equal(now) {
		t.Errorf("identity last used %v, want %v", used, now)
	}
}

func TestALinkedStaffAccountIsRefusedWithoutTheHomeDirectory(t *testing.T) {
	f := newFixture(t)
	org := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Roles: []string{"organizer"}})
	f.link(t, org, "sub-ana")

	if _, err := f.service().Resolve(t.Context(), trusted("sub-ana")); !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.IsZero() {
		t.Errorf("a refused sign-in stamped the identity at %v", used)
	}
}

// A student linked through a provider that has no home directory and made
// staff afterwards stays out: only a home directory sign-in enters staff.
func TestAStudentLinkedThroughGitHubAndMadeStaffIsRefused(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Roles: []string{users.RoleStudent}})
	const githubIssuer = "https://github.com"
	if err := f.repo.LinkExternalIdentity(t.Context(), users.ExternalIdentity{
		UserID: ana.ID, Provider: users.ProviderGitHub, Issuer: githubIssuer, Subject: "4242",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ReplaceRoles(t.Context(), ana.ID, []string{"organizer"}); err != nil {
		t.Fatal(err)
	}

	_, err := f.service().Resolve(t.Context(), federation.Claims{
		Provider: users.ProviderGitHub, Issuer: githubIssuer, Subject: "4242",
		Email: "ana@example.edu", EmailTrusted: true,
	})

	if !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if used := f.repo.LastUsed(users.ProviderGitHub, githubIssuer, "4242"); !used.IsZero() {
		t.Errorf("a refused sign-in stamped the identity at %v", used)
	}
}

func TestTheHomeDirectoryNeverOpensABlockedOrDeletedAccount(t *testing.T) {
	for name, tc := range map[string]struct {
		status string
		want   error
	}{
		"blocked": {users.StatusBlocked, users.ErrAccountBlocked},
		"deleted": {users.StatusDeleted, users.ErrAccountDeleted},
	} {
		t.Run(name+", known identity", func(t *testing.T) {
			f := newFixture(t)
			org := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Status: tc.status, Roles: []string{"organizer"}})
			f.link(t, org, "sub-ana")

			if _, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana")); !errors.Is(err, tc.want) {
				t.Errorf("Resolve() = %v, want %v", err, tc.want)
			}
			if used := f.repo.LastUsed(users.ProviderMicrosoft, issuer, "sub-ana"); !used.IsZero() {
				t.Errorf("a refused sign-in stamped the identity at %v", used)
			}
		})
	}
	t.Run("blocked, trusted email", func(t *testing.T) {
		f := newFixture(t)
		org := f.repo.Add(users.User{Login: "organizer", FullName: "Org", Email: "ana@example.edu", Status: users.StatusBlocked, Roles: []string{"organizer"}})

		if _, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana")); !errors.Is(err, users.ErrAccountBlocked) {
			t.Errorf("Resolve() = %v, want ErrAccountBlocked", err)
		}
		if len(f.repo.Identities(org.ID)) != 0 {
			t.Error("the blocked account was linked")
		}
	})
}

func TestTheHomeDirectoryFlagReachesTheLink(t *testing.T) {
	for _, home := range []bool{true, false} {
		f := newFixture(t)
		f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
		c := trusted("sub-ana")
		c.HomeDirectory = home

		if _, err := f.service().Resolve(t.Context(), c); err != nil {
			t.Fatalf("home %v: Resolve() = %v", home, err)
		}
		if len(f.accounts.linked) != 1 || f.accounts.linked[0].HomeDirectory != home {
			t.Errorf("home %v: link commands = %+v, want one carrying the flag", home, f.accounts.linked)
		}
	}
}

func TestAnAccountMadeStaffBeforeAHomeDirectoryLinkIsLinked(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.accounts.before = func() {
		if err := f.repo.ReplaceRoles(t.Context(), ana.ID, []string{"organizer"}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := f.service().Resolve(t.Context(), homeDirectory("sub-ana"))

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 1 {
		t.Errorf("identities = %+v, want one", linked)
	}
}

// claimingAccounts answers CreateExternal as the unique email index does when
// a live account took the address after Resolve looked: appear, when set, is
// that account.
type claimingAccounts struct {
	federation.Accounts
	repo    *userstest.Repository
	appear  *users.User
	creates int
}

func (a *claimingAccounts) CreateExternal(ctx context.Context, cmd users.CreateExternalCommand) (users.User, error) {
	a.creates++
	if a.appear != nil {
		a.repo.Add(*a.appear)
		a.appear = nil
	}
	if cmd.Email != "" {
		if _, err := a.repo.ByEmail(ctx, cmd.Email); err == nil {
			return users.User{}, users.ErrEmailTaken
		}
	}
	return a.Accounts.CreateExternal(ctx, cmd)
}

func TestAnAccountThatTakesTheEmailDuringCreationIsLinkedInstead(t *testing.T) {
	f := newFixture(t)
	ana := users.User{ID: uuid.New(), Login: "confirmed", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}}
	accounts := &claimingAccounts{Accounts: f.accounts, repo: f.repo, appear: &ana}
	f.accounts = &hookedAccounts{Accounts: accounts}

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want the account that took the address", got.ID, err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 1 {
		t.Errorf("identities = %+v, want sub-ana linked to it", linked)
	}
	if n := f.accountCount(); n != 1 {
		t.Errorf("accounts = %d, want only the one that took the address", n)
	}
}

func TestAStaffAccountThatTakesTheEmailDuringCreationIsRefused(t *testing.T) {
	f := newFixture(t)
	staff := users.User{ID: uuid.New(), Login: "organizer", FullName: "Org", Email: "ana@example.edu", Roles: []string{"organizer"}}
	f.accounts = &hookedAccounts{Accounts: &claimingAccounts{Accounts: f.accounts, repo: f.repo, appear: &staff}}

	if _, err := f.service().Resolve(t.Context(), trusted("sub-ana")); !errors.Is(err, federation.ErrStaffAccount) {
		t.Errorf("Resolve() = %v, want ErrStaffAccount", err)
	}
	if linked := f.repo.Identities(staff.ID); len(linked) != 0 {
		t.Errorf("identities = %+v, want none", linked)
	}
}

// Resolution is re-run once; a second refusal is the answer.
func TestATakenEmailTwiceIsTheAnswer(t *testing.T) {
	f := newFixture(t)
	accounts := &claimingAccounts{Accounts: f.accounts, repo: f.repo}
	f.store.Store = vanishingEmails{f.repo}
	f.repo.Add(users.User{Login: "holder", FullName: "Holder", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.accounts = &hookedAccounts{Accounts: accounts}

	if _, err := f.service().Resolve(t.Context(), trusted("sub-ana")); !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("Resolve() = %v, want ErrEmailTaken", err)
	}
	if accounts.creates != 2 {
		t.Errorf("creations tried = %d, want the first and one more", accounts.creates)
	}
}

// vanishingEmails never finds an account by email, as though each one were
// gone again by the time Resolve looked.
type vanishingEmails struct{ federation.Store }

func (vanishingEmails) ByEmail(context.Context, string) (users.User, error) {
	return users.User{}, users.ErrNotFound
}

// The account Resolve found by its address moved to another before the link:
// the address is then nobody's, and a new account takes it.
func TestAnAccountReaddressedBeforeTheLinkIsNotLinked(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.accounts.before = func() {
		if err := f.repo.UpdateProfile(t.Context(), ana.ID, "Ana", "elsewhere@example.edu", false); err != nil {
			t.Fatal(err)
		}
	}

	got, err := f.service().Resolve(t.Context(), trusted("sub-ana"))
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	if got.ID == ana.ID || len(f.repo.Identities(ana.ID)) != 0 {
		t.Error("the identity was linked to an account no longer holding the address")
	}
	if stored, _ := f.repo.Get(got.ID); stored.Email != "ana@example.edu" || stored.EmailVerifiedAt == nil {
		t.Errorf("new account %+v, want the trusted address, verified", stored)
	}
}

func TestASecondIdentityOfTheSameIssuerIsNotLinkedByEmail(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.link(t, ana, "sub-previous-holder")

	_, err := f.service().Resolve(t.Context(), trusted("sub-new-holder"))

	if !errors.Is(err, federation.ErrIdentityConflict) {
		t.Errorf("Resolve() = %v, want ErrIdentityConflict", err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 1 || linked[0].Subject != "sub-previous-holder" {
		t.Errorf("identities = %+v, want only the previous holder's", linked)
	}
	if n := f.accountCount(); n != 1 {
		t.Errorf("accounts = %d, want no new one", n)
	}
}

func TestAnIdentityOfAnotherIssuerIsLinkedByEmail(t *testing.T) {
	f := newFixture(t)
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	f.link(t, ana, "sub-ana")
	c := trusted("sub-ana-elsewhere")
	c.Issuer = "https://login.example.edu/other-tenant/v2.0"

	got, err := f.service().Resolve(t.Context(), c)

	if err != nil || got.ID != ana.ID {
		t.Fatalf("Resolve() = %v, %v; want ana's account", got.ID, err)
	}
	if linked := f.repo.Identities(ana.ID); len(linked) != 2 {
		t.Errorf("identities = %+v, want both issuers linked", linked)
	}
}
