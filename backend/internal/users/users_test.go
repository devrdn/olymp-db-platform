package users_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	// Every exported sentinel reaches callers; errUnhandledSkipReason is
	// unexported.
	sentineltest.AssertListed(t, ".")
}

// CLAUDE.md rule 2: the query runs through trigram ILIKEs on every listing.
// The bound counts characters, as pg_trgm does, so a Cyrillic query at the
// limit passes although it is twice as many bytes.
func TestFilterValidateBoundsTheQueryInCharacters(t *testing.T) {
	for name, query := range map[string]string{
		"empty":                  "",
		"at the limit":           strings.Repeat("a", users.MaxQueryLength),
		"at the limit, Cyrillic": strings.Repeat("ф", users.MaxQueryLength),
	} {
		if err := (users.Filter{Query: query}).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want no error", name, err)
		}
	}
	for name, query := range map[string]string{
		"over the limit":           strings.Repeat("a", users.MaxQueryLength+1),
		"over the limit, Cyrillic": strings.Repeat("ф", users.MaxQueryLength+1),
	} {
		if err := (users.Filter{Query: query}).Validate(); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidAccount", name, err)
		}
	}
}

func TestHasVerifiedEmailInAcceptsTheDomainAndItsSubdomains(t *testing.T) {
	verified := time.Now()
	cases := []struct {
		name   string
		user   users.User
		domain string
		want   bool
	}{
		{"the domain itself", users.User{Email: "a@example.edu", EmailVerifiedAt: &verified}, "example.edu", true},
		{"a subdomain", users.User{Email: "a@stud.example.edu", EmailVerifiedAt: &verified}, "example.edu", true},
		{"another domain ending the same way", users.User{Email: "a@notexample.edu", EmailVerifiedAt: &verified}, "example.edu", false},
		{"not verified", users.User{Email: "a@example.edu"}, "example.edu", false},
		{"upper case", users.User{Email: "A@EXAMPLE.EDU", EmailVerifiedAt: &verified}, "example.edu", true},
		{"an empty domain", users.User{Email: "a@example.edu", EmailVerifiedAt: &verified}, "", false},
		{"no email", users.User{EmailVerifiedAt: &verified}, "example.edu", false},
	}
	for _, c := range cases {
		if got := c.user.HasVerifiedEmailIn(c.domain); got != c.want {
			t.Errorf("%s: HasVerifiedEmailIn(%q) = %v, want %v", c.name, c.domain, got, c.want)
		}
	}
}

func TestIsStaffIsAnyPermission(t *testing.T) {
	if (users.User{Roles: []string{"student"}}).IsStaff() {
		t.Error("an account without permissions is staff")
	}
	if !(users.User{Permissions: []string{"contest.view"}}).IsStaff() {
		t.Error("an account holding a permission is not staff")
	}
}

func TestHasPasswordIsFalseForAnAccountWithoutADigest(t *testing.T) {
	if (users.User{}).HasPassword() || !(users.User{PasswordHash: "x"}).HasPassword() {
		t.Error("HasPassword does not follow the digest")
	}
}

func TestExternalIdentityValidateBoundsEveryField(t *testing.T) {
	ok := users.ExternalIdentity{UserID: uuid.New(), Provider: users.ProviderMicrosoft, Issuer: "https://issuer.example", Subject: "abc", Email: "a@example.edu"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want no error", err)
	}
	long := strings.Repeat("a", users.MaxIdentityFieldLength+1)
	for name, id := range map[string]users.ExternalIdentity{
		"unknown provider": {UserID: ok.UserID, Provider: "other", Issuer: "i", Subject: "s"},
		"empty subject":    {UserID: ok.UserID, Provider: users.ProviderMicrosoft, Issuer: "i"},
		"long issuer":      {UserID: ok.UserID, Provider: users.ProviderMicrosoft, Issuer: long, Subject: "s"},
		"long subject":     {UserID: ok.UserID, Provider: users.ProviderMicrosoft, Issuer: "i", Subject: long},
		"long email":       {UserID: ok.UserID, Provider: users.ProviderMicrosoft, Issuer: "i", Subject: "s", Email: strings.Repeat("a", users.MaxEmailLength+1)},
	} {
		if err := id.Validate(); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidAccount", name, err)
		}
	}
}

// A name greets the reader of mail sent to any address, so it may carry
// nothing a link or a pitch needs.
func TestANameIsWrittenOnlyWithWhatNamesAreWrittenWith(t *testing.T) {
	for _, taken := range []string{
		"Ana-Maria Popescu", "O'Neil", "D’Artagnan", "Jr.", "J. R. R. Tolkien", "Ștefan Țurcan", "Алёна", "Zoë", "René",
	} {
		if !users.IsWrittenAsAName(taken) {
			t.Errorf("IsWrittenAsAName(%q) = false, want it taken", taken)
		}
	}
	for _, refused := range []string{
		"", "https://evil.example", "click here!", "win $100", "Ana2", "a/b", "Ana<b>", "Ana:", "Ana@x", "Ana_B",
		"Ana B", "Ana\tB", "evil.com", "Ștefan.Țurcan", "J.R.", "Visit evil.example now", "\xff",
	} {
		if users.IsWrittenAsAName(refused) {
			t.Errorf("IsWrittenAsAName(%q) = true, want it refused", refused)
		}
	}
}

func TestOnlyALiveStudentWithAnAddressIsReachableByEmail(t *testing.T) {
	for name, tc := range map[string]struct {
		user    users.User
		manages bool
		want    bool
	}{
		"a student":         {users.User{Status: users.StatusActive, Email: "ana@example.edu"}, false, true},
		"staff":             {users.User{Status: users.StatusActive, Email: "ana@example.edu", Permissions: []string{"contests.manage"}}, false, false},
		"a contest manager": {users.User{Status: users.StatusActive, Email: "ana@example.edu"}, true, false},
		"blocked":           {users.User{Status: users.StatusBlocked, Email: "ana@example.edu"}, false, false},
		"deleted":           {users.User{Status: users.StatusDeleted, Email: "ana@example.edu"}, false, false},
		"without an email":  {users.User{Status: users.StatusActive}, false, false},
	} {
		got, err := users.ReachableByEmail(t.Context(), managing(tc.manages), tc.user)
		if err != nil || got != tc.want {
			t.Errorf("%s: ReachableByEmail() = %v, %v, want %v", name, got, err, tc.want)
		}
	}
}

func TestAContestManagerActsAsStaffWithoutAGlobalRole(t *testing.T) {
	for name, tc := range map[string]struct {
		user    users.User
		manages bool
		want    bool
	}{
		"a student":         {users.User{}, false, false},
		"a global role":     {users.User{Permissions: []string{"contests.manage"}}, false, true},
		"a contest manager": {users.User{}, true, true},
	} {
		got, err := users.ActsAsStaff(t.Context(), managing(tc.manages), tc.user)
		if err != nil || got != tc.want {
			t.Errorf("%s: ActsAsStaff() = %v, %v, want %v", name, got, err, tc.want)
		}
	}
}

// The lookup runs for every account, so its time does not tell a student
// from staff or from nobody.
func TestReachableByEmailAsksAboutContestsWhateverTheAccount(t *testing.T) {
	for name, u := range map[string]users.User{
		"nobody":  {},
		"blocked": {Status: users.StatusBlocked, Email: "ana@example.edu"},
		"staff":   {Status: users.StatusActive, Email: "ana@example.edu", Permissions: []string{"contests.manage"}},
	} {
		asked := &askedContests{}
		if _, err := users.ReachableByEmail(t.Context(), asked, u); err != nil || asked.calls != 1 {
			t.Errorf("%s: asked %d times (%v), want once", name, asked.calls, err)
		}
	}
}

func TestReachableByEmailPassesALookupFailureOn(t *testing.T) {
	failure := errors.New("database down")
	_, err := users.ReachableByEmail(t.Context(), failingContests{failure}, users.User{Status: users.StatusActive, Email: "ana@example.edu"})
	if !errors.Is(err, failure) {
		t.Errorf("ReachableByEmail() = %v, want the lookup's failure", err)
	}
}

type managing bool

func (m managing) ManagesContest(context.Context, uuid.UUID) (bool, error) { return bool(m), nil }

type askedContests struct{ calls int }

func (a *askedContests) ManagesContest(context.Context, uuid.UUID) (bool, error) {
	a.calls++
	return false, nil
}

type failingContests struct{ err error }

func (f failingContests) ManagesContest(context.Context, uuid.UUID) (bool, error) {
	return false, f.err
}
