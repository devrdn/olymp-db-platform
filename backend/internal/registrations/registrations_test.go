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
	longest := strings.Repeat("ș", registrations.MaxNameRunes)
	if _, err := registrations.Parse(longest, longest, "ana@example.com"); err != nil {
		t.Fatalf("names of %d letters: Parse() = %v, want them taken", registrations.MaxNameRunes, err)
	}

	for name, value := range map[string]string{
		"empty":             "   ",
		"too long":          longest + "a",
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

func TestDisplayCodeSplitsTheCodeInTwo(t *testing.T) {
	if got := registrations.DisplayCode("K7M2QX"); got != "K7M-2QX" {
		t.Errorf("DisplayCode() = %q, want K7M-2QX", got)
	}
}
