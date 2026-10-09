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
	NewCode("conflict_test", "The first meaning.")

	defer func() {
		if recover() == nil {
			t.Error("declaring a conflicting meaning was accepted, want a panic")
		}
	}()
	NewCode("conflict_test", "A different meaning entirely.")
}

func TestACodeMustLookLikeACode(t *testing.T) {
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
	defer func() {
		if recover() == nil {
			t.Error("a code with no meaning was accepted, want a panic")
		}
	}()
	NewCode("unexplained_test", "  ")
}

func TestTheCatalogComesBackSortedAndComplete(t *testing.T) {
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
