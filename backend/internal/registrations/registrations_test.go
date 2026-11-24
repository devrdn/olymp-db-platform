package registrations_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/registrations"
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
		"empty":                         "   ",
		"too many letters":              longest + "a",
		"control character":             "Ana\x00",
		"line break":                    "Ana\nBcc",
		"invalid UTF-8":                 "Ana\xff",
		"a Hangul filler":               "\u3164",
		"a filler in a name":            "Ana\uffa0",
		"only a mark":                   "\u0301",
		"a Khmer inherent vowel":        "Ana\u17b4",
		"a joiner in a Latin name":      "Ion\u200cPopescu",
		"a trailing joiner":             "\u0639\u0644\u06cc\u200c",
		"a Mongolian selector in Latin": "Ana\u180bb",
		"a final joiner after Latin":    "Ana\u200d",
		"a variation sel.":              "Ana\ufe0f",
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
func TestParseTakesANameItsScriptJoins(t *testing.T) {
	for _, name := range []string{"\u0d30\u0d3e\u0d1c\u0d28\u0d4d\u200d", "\u09b6\u09b0\u09a4\u09cd\u200d", "\u182e\u1823\u180b\u1829\u182d\u1823\u182f", "\u182d\u1820\u180e\u1820", "\u0639\u0644\u06cc\u200c\u0631\u0636\u0627", "\u0936\u094d\u200d\u0930\u0940"} {
		if _, err := registrations.Parse(name, "Popescu", "ana@example.com"); err != nil {
			t.Errorf("Parse(%q) = %v, want it taken", name, err)
		}
	}
}

func TestParseTakesOnlyTheCharactersOfAName(t *testing.T) {
	for _, taken := range []string{"Ana-Maria", "O'Neil", "D’Artagnan", "Jr.", "J. R. R.", "Ștefan Țurcan", "Алёна", "Zoë", "René"} {
		if _, err := registrations.Parse(taken, "Popescu", "ana@example.com"); err != nil {
			t.Errorf("Parse(%q) = %v, want it taken", taken, err)
		}
	}
	for _, refused := range []string{
		"https://evil.example", "click here!", "win $100", "Ana2", "a/b", "Ana<b>", "Ana:", "Ana@x", "Ana_B",
		"Ana B", "Ana\tB",
		"evil.com", "Ștefan.Țurcan", "J.R.",
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
		"empty":                "",
		"no at sign":           "ana.example.com",
		"two at signs":         `"a@b"@example.com`,
		"display name":         "Ana <ana@example.com>",
		"two":                  "ana@example.com, bob@example.com",
		"too long":             local + "a@example.com",
		"line break":           "ana@example.com\nBcc: x@example.com",
		"a zero-width space":   "a\u200bna@example.com",
		"a bidi override":      "ana@exa\u202emple.com",
		"a grapheme joiner":    "ana\u034f@example.com",
		"a Mongolian selector": "ana\u180b@example.com",
		"a variation sel.":     "ana\ufe0f@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registrations.Parse("Ana", "Popescu", value); !errors.Is(err, registrations.ErrInvalidEmail) {
				t.Errorf("Parse(%q) = %v, want ErrInvalidEmail", value, err)
			}
		})
	}
}

// U+0130 and the Kelvin sign lower-case to an ASCII letter, so the key, and
// the login made of it, would name another mailbox than the one typed; U+023A
// grows instead, and must not hide a letter that shrinks.
func TestParseRefusesAnAddressLowerCasingWouldTurnIntoAnother(t *testing.T) {
	for _, email := range []string{"\u0130on.popescu@usm.md", "\u212Aate@usm.md", "\u023Aa@usm.md", "\u0130on\u023A@usm.md"} {
		if _, err := registrations.Parse("Ana", "Popescu", email); !errors.Is(err, registrations.ErrInvalidEmail) {
			t.Errorf("Parse(%q) = %v, want ErrInvalidEmail", email, err)
		}
	}
	if _, err := registrations.Parse("Ana", "Popescu", "Ștefan@usm.md"); err != nil {
		t.Errorf("an address whose letters keep their width: Parse() = %v, want it taken", err)
	}
}

func TestDisplayCodeSplitsTheCodeInTwo(t *testing.T) {
	if got := registrations.DisplayCode("K7M2QX"); got != "K7M-2QX" {
		t.Errorf("DisplayCode() = %q, want K7M-2QX", got)
	}
}
