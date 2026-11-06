package registrations_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/registrations"
)

func TestParseTrimsAndKeysTheEmailInLowerCase(t *testing.T) {
	a, err := registrations.Parse("  Ana ", " Popescu\t", "  Ana.Popescu@Example.com ")
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	want := registrations.Applicant{
		FirstName: "Ana", LastName: "Popescu",
		Email: "Ana.Popescu@Example.com", EmailKey: "ana.popescu@example.com",
	}
	if a != want {
		t.Errorf("Parse() = %+v, want %+v", a, want)
	}
	if a.FullName() != "Ana Popescu" {
		t.Errorf("FullName() = %q", a.FullName())
	}
}

func TestParseBoundsTheNames(t *testing.T) {
	longest := strings.Repeat("a", registrations.MaxNameRunes)
	if _, err := registrations.Parse(longest, longest[1:], "ana@example.com"); err != nil {
		t.Fatalf("names of %d letters: Parse() = %v, want them taken", registrations.MaxNameRunes, err)
	}

	for name, value := range map[string]string{
		"empty":             "   ",
		"too many letters":  longest + "a",
		"control character": "Ana\x00",
		"line break":        "Ana\nBcc",
		"invalid UTF-8":     "Ana\xff",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registrations.Parse(value, "Popescu", "ana@example.com"); !errors.Is(err, registrations.ErrInvalidName) {
				t.Errorf("first name: Parse() = %v, want ErrInvalidName", err)
			}
			if _, err := registrations.Parse("Ana", value, "ana@example.com"); !errors.Is(err, registrations.ErrInvalidName) {
				t.Errorf("last name: Parse() = %v, want ErrInvalidName", err)
			}
		})
	}
}

// Each name is within its letters, but the account's full name is bounded in
// bytes, and a Romanian or Cyrillic letter takes two.
func TestParseBoundsTheFullNameInBytes(t *testing.T) {
	fifty := strings.Repeat("ș", 50)

	if _, err := registrations.Parse(fifty, strings.Repeat("ș", 49)+"a", "ana@example.com"); err != nil {
		t.Errorf("a full name of %d bytes: Parse() = %v, want it taken", registrations.MaxFullNameBytes, err)
	}
	if _, err := registrations.Parse(fifty, fifty, "ana@example.com"); !errors.Is(err, registrations.ErrInvalidName) {
		t.Errorf("a full name of %d bytes: Parse() = %v, want ErrInvalidName", registrations.MaxFullNameBytes+1, err)
	}
}

// The names are the greeting of two messages sent to any address, so they
// may carry nothing a link or a pitch needs.
func TestParseTakesOnlyTheCharactersOfAName(t *testing.T) {
	for _, taken := range []string{"Ana-Maria", "O'Neil", "D’Artagnan", "Jr.", "J. R. R.", "Ștefan Țurcan", "Алёна", "Zoë", "René"} {
		if _, err := registrations.Parse(taken, "Popescu", "ana@example.com"); err != nil {
			t.Errorf("Parse(%q) = %v, want it taken", taken, err)
		}
	}
	for _, refused := range []string{
		"https://evil.example", "click here!", "win $100", "Ana2", "a/b", "Ana<b>", "Ana:", "Ana@x", "Ana_B",
		"Ana B", "Ana\tB",
	} {
		if _, err := registrations.Parse("Ana", refused, "ana@example.com"); !errors.Is(err, registrations.ErrInvalidName) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidName", refused, err)
		}
	}
}

func TestParseRefusesAnythingButOneBareAddress(t *testing.T) {
	local := strings.Repeat("a", registrations.MaxEmailBytes-len("@example.com"))
	if _, err := registrations.Parse("Ana", "Popescu", local+"@example.com"); err != nil {
		t.Fatalf("an address of %d bytes: Parse() = %v, want it taken", registrations.MaxEmailBytes, err)
	}

	for name, value := range map[string]string{
		"empty":        "",
		"no at sign":   "ana.example.com",
		"two at signs": `"a@b"@example.com`,
		"display name": "Ana <ana@example.com>",
		"two":          "ana@example.com, bob@example.com",
		"too long":     local + "a@example.com",
		"line break":   "ana@example.com\nBcc: x@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registrations.Parse("Ana", "Popescu", value); !errors.Is(err, registrations.ErrInvalidEmail) {
				t.Errorf("Parse(%q) = %v, want ErrInvalidEmail", value, err)
			}
		})
	}
}

// U+023A is two bytes and its lower case, U+2C65, three: an address within
// the bound can lowercase past the login's.
func TestParseBoundsTheEmailKeyAfterLowercasing(t *testing.T) {
	email := "Ⱥ" + strings.Repeat("a", registrations.MaxEmailBytes-len("@example.com")-2) + "@example.com"
	if len(email) != registrations.MaxEmailBytes {
		t.Fatalf("the address is %d bytes, want %d", len(email), registrations.MaxEmailBytes)
	}
	if len(strings.ToLower(email)) <= registrations.MaxEmailBytes {
		t.Fatal("the lowercased address does not grow; the test proves nothing")
	}

	if _, err := registrations.Parse("Ana", "Popescu", email); !errors.Is(err, registrations.ErrInvalidEmail) {
		t.Errorf("Parse() = %v, want ErrInvalidEmail", err)
	}
}

func TestDisplayCodeSplitsTheCodeInTwo(t *testing.T) {
	if got := registrations.DisplayCode("K7M2QX"); got != "K7M-2QX" {
		t.Errorf("DisplayCode() = %q, want K7M-2QX", got)
	}
}
