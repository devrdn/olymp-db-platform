package httpx

import (
	"slices"
	"strings"
	"testing"
)

func TestADeclaredCodeCarriesItsValue(t *testing.T) {
	code := NewCode("teapot_test", "Nothing real; declared by a test.")

	if code.String() != "teapot_test" {
		t.Errorf("String() = %q, want teapot_test", code.String())
	}
}

func TestDeclaringTheSameCodeTwiceReturnsTheSameCode(t *testing.T) {
	// Two packages legitimately answer with the same code — "not found" is the
	// router's 404 and a handler's missing contest. Re-declaring must be the
	// same code, not a second entry in the catalog.
	first := NewCode("shared_test", "Declared twice on purpose.")
	second := NewCode("shared_test", "Declared twice on purpose.")

	if first != second {
		t.Errorf("the same code declared twice produced %v and %v", first, second)
	}

	count := 0
	for _, entry := range Catalog() {
		if entry.Code == "shared_test" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("catalog holds %d entries for one code, want 1", count)
	}
}

func TestDeclaringOneCodeWithTwoMeaningsIsRefused(t *testing.T) {
	// The catalog is published as the API's contract. Two meanings for one
	// code would make it a document that cannot be true, and the failure has
	// to arrive at startup rather than in whichever client read it first.
	NewCode("conflict_test", "The first meaning.")

	defer func() {
		if recover() == nil {
			t.Error("declaring a conflicting meaning was accepted, want a panic")
		}
	}()
	NewCode("conflict_test", "A different meaning entirely.")
}

func TestACodeMustLookLikeACode(t *testing.T) {
	// The value travels to clients that switch on it, and it is written into
	// a generated contract file; anything but lower_snake_case is a typo.
	for _, value := range []string{"", "Not_Lower", "has space", "has-dash", "trailing_"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewCode(%q) was accepted, want a panic", value)
				}
			}()
			NewCode(value, "irrelevant")
		}()
	}
}

func TestACodeMustExplainItself(t *testing.T) {
	// The meaning is what the contract file is for: a client author reading it
	// has to learn when the code arrives, and an empty string teaches nothing.
	defer func() {
		if recover() == nil {
			t.Error("a code with no meaning was accepted, want a panic")
		}
	}()
	NewCode("unexplained_test", "  ")
}

func TestTheCatalogComesBackSortedAndComplete(t *testing.T) {
	// It is generated into a file that is committed and reviewed; an unstable
	// order would produce a diff on every build and hide the real changes.
	NewCode("zzz_last_test", "Sorts last.")
	NewCode("aaa_first_test", "Sorts first.")

	catalog := Catalog()
	codes := make([]string, 0, len(catalog))
	for _, entry := range catalog {
		codes = append(codes, entry.Code)
	}

	if !slices.IsSorted(codes) {
		t.Errorf("Catalog() is not sorted: %v", codes)
	}
	if !slices.Contains(codes, "aaa_first_test") || !slices.Contains(codes, "zzz_last_test") {
		t.Errorf("Catalog() = %v, want both declared codes", codes)
	}
	for _, entry := range catalog {
		if strings.TrimSpace(entry.Meaning) == "" {
			t.Errorf("code %q has no meaning in the catalog", entry.Code)
		}
	}
}
