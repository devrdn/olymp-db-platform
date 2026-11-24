package users_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
)

// A typed login keeps whatever shape its account was made with.
func TestATypedLoginIsTrimmedAndLowerCasedAndKeepsItsDot(t *testing.T) {
	if got := users.ParseTypedLogin("  Ion.Popescu\t").String(); got != "ion.popescu" {
		t.Errorf("ParseTypedLogin() = %q, want ion.popescu", got)
	}
}

// A login and an address are keyed alike, so a recovery or sign-up throttle
// counts what its lookup compares.
func TestATypedKeyIsTrimmedAndLowerCasedWhateverItNames(t *testing.T) {
	for raw, want := range map[string]string{
		"  Ion.Popescu\t": "ion.popescu", " Ana@USM.md ": "ana@usm.md", "\u0218TEFAN": "\u0219tefan", "": "",
	} {
		if got := users.TypedKey(raw); got != want {
			t.Errorf("TypedKey(%q) = %q, want %q", raw, got, want)
		}
		if got := users.ParseTypedLogin(raw).String(); got != want {
			t.Errorf("ParseTypedLogin(%q) = %q, want %q, the key", raw, got, want)
		}
	}
}

func TestANewLoginIsTrimmedAndKeepsItsLetterCase(t *testing.T) {
	got, err := users.ParseNewLogin("  Ion_Popescu ")
	if err != nil || got.String() != "Ion_Popescu" {
		t.Errorf("ParseNewLogin() = %q, %v; want Ion_Popescu", got, err)
	}
}

// The refusal names its cause, and is still an invalid account to every
// caller that reads it as one.
func TestANewLoginMayNotHoldADot(t *testing.T) {
	for _, login := range []string{
		"ion.popescu", ".ana", "ana.", "a.b.c", " ion.popescu ",
		"evil\u3002com", "evil\uff0ecom", "evil\uff61com", "evil\u2024com", "evil\ufe52com",
	} {
		_, err := users.ParseNewLogin(login)
		if !errors.Is(err, users.ErrLoginHasDot) || !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("ParseNewLogin(%q) = %v, want ErrLoginHasDot, an ErrInvalidAccount", login, err)
		}
	}
}

// UTS #46 maps the first four to a dot when it reads a domain name, and NFKC
// folds the last two to one, so each would make the same link.
func TestTheLabelSeparatorsAreTheDotAndWhatIDNAMapsToIt(t *testing.T) {
	for _, r := range []rune{'.', '\u3002', '\uff0e', '\uff61', '\u2024', '\ufe52'} {
		if !users.IsLabelSeparator(r) {
			t.Errorf("IsLabelSeparator(%U) = false, want true", r)
		}
	}
	for _, r := range []rune{'-', '_', ',', '\u00b7', '\ufe12'} {
		if users.IsLabelSeparator(r) {
			t.Errorf("IsLabelSeparator(%U) = true, want false", r)
		}
	}
}

// A letter whose lower case has another width is the sign of a fold such as
// U+0130 or the Kelvin sign to ASCII; two such letters must not cancel out.
func TestLowerCaseKeepsWidthLooksAtEveryLetter(t *testing.T) {
	for _, s := range []string{"ana@usm.md", "Ana.Popescu@USM.md", "\u0218tefan@usm.md", "\u0410\u043b\u0451\u043d\u0430"} {
		if !users.LowerCaseKeepsWidth(s) {
			t.Errorf("LowerCaseKeepsWidth(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"\u0130on@usm.md", "\u212Aate@usm.md", "\u023Aa@usm.md", "\u0130on\u023A@usm.md", "Kate@\u023A\u023A.md"} {
		if users.LowerCaseKeepsWidth(s) {
			t.Errorf("LowerCaseKeepsWidth(%q) = true, want false", s)
		}
	}
}

// The dot is the only shape the owner ruled out: what administrators already
// type, letter case and other alphabets included, stays accepted.
func TestANewLoginKeepsEverythingButTheDot(t *testing.T) {
	for _, login := range []string{"ion-popescu", "Ion_Popescu", "student01", "иванов", "a"} {
		if _, err := users.ParseNewLogin(login); err != nil {
			t.Errorf("ParseNewLogin(%q) = %v, want no error", login, err)
		}
	}
}

func TestANewLoginIsBoundedAndHoldsNoControlOrFormatCharacter(t *testing.T) {
	for name, login := range map[string]string{
		"empty":              "",
		"only spaces":        "   ",
		"over the bound":     strings.Repeat("a", users.MaxLoginLength+1),
		"a NUL":              "ana\x00",
		"a bidi override":    "ana\u202epopescu",
		"a bidi isolate":     "ana\u2066popescu",
		"a zero-width space": "ana\u200bpopescu",
	} {
		if _, err := users.ParseNewLogin(login); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("%s: ParseNewLogin() = %v, want ErrInvalidAccount", name, err)
		}
	}
	if _, err := users.ParseNewLogin(strings.Repeat("a", users.MaxLoginLength)); err != nil {
		t.Errorf("a login at the bound: ParseNewLogin() = %v, want no error", err)
	}
}

func TestAChosenLoginIsShortPlainAndLowerCase(t *testing.T) {
	for raw, want := range map[string]string{
		"ana": "ana", "ana-maria_01": "ana-maria_01", "0ana": "0ana", "  Ana-Popescu ": "ana-popescu",
		strings.Repeat("a", 64): strings.Repeat("a", 64),
	} {
		if got, err := users.ParseChosenLogin(raw); err != nil || got.String() != want {
			t.Errorf("ParseChosenLogin(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, login := range []string{
		"ion.popescu", "ab", strings.Repeat("a", 65), "-ana", "_ana", "ana popescu", "ana@usm", "иванов", "ana\u200b",
	} {
		if _, err := users.ParseChosenLogin(login); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("ParseChosenLogin(%q) = %v, want ErrInvalidAccount", login, err)
		}
	}
}

func parsedLogin(t *testing.T, raw string) users.Login {
	t.Helper()
	login, err := users.ParseNewLogin(raw)
	if err != nil {
		t.Fatalf("ParseNewLogin(%q) = %v", raw, err)
	}
	return login
}

func TestRefuseTakenLoginRefusesALiveAccountsLoginInAnyCase(t *testing.T) {
	repo := userstest.New()
	repo.Add(users.User{Login: "Ana", FullName: "Ana", Status: users.StatusActive})

	if err := users.RefuseTakenLogin(t.Context(), repo, parsedLogin(t, "ana")); !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("RefuseTakenLogin() = %v, want ErrLoginTaken", err)
	}
	if err := users.RefuseTakenLogin(t.Context(), repo, parsedLogin(t, "ion")); err != nil {
		t.Errorf("RefuseTakenLogin() of a free login = %v, want no error", err)
	}
}

func TestRefuseTakenLoginFreesADeletedAccountsLogin(t *testing.T) {
	repo := userstest.New()
	repo.Add(users.User{Login: "ana", FullName: "Ana", Status: users.StatusDeleted})

	if err := users.RefuseTakenLogin(t.Context(), repo, parsedLogin(t, "ana")); err != nil {
		t.Errorf("RefuseTakenLogin() = %v, want a deleted account's login free", err)
	}
}

func TestRefuseTakenLoginPassesAStorageFailureOn(t *testing.T) {
	repo := userstest.New()
	outage := errors.New("connection refused")
	repo.Err = outage

	if err := users.RefuseTakenLogin(t.Context(), repo, parsedLogin(t, "ana")); !errors.Is(err, outage) {
		t.Errorf("RefuseTakenLogin() = %v, want the storage error", err)
	}
}
