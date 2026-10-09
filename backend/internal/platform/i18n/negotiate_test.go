package i18n

import (
	"reflect"
	"testing"
)

func TestExactPreferenceWins(t *testing.T) {
	got := Match([]string{"ro"}, []string{"en", "ro", "ru"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want ro", got)
	}
}

func TestPreferenceOrderIsHonoured(t *testing.T) {
	got := Match([]string{"kk", "ru", "en"}, []string{"en", "ru"}, "en")

	if got != "ru" {
		t.Errorf("Match() = %q, want ru — kk is unavailable, ru is the next preference", got)
	}
}

func TestMatchIsCaseInsensitive(t *testing.T) {
	// Accept-Language is case-insensitive (RFC 9110).
	got := Match([]string{"RO"}, []string{"en", "ro"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want ro", got)
	}
}

func TestRegionalPreferenceFallsBackToItsBaseLanguage(t *testing.T) {
	got := Match([]string{"ro-MD"}, []string{"en", "ro", "ru"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want ro", got)
	}
}

func TestExactRegionalMatchBeatsBaseLanguage(t *testing.T) {
	got := Match([]string{"ru-KZ"}, []string{"ru", "ru-KZ"}, "en")

	if got != "ru-KZ" {
		t.Errorf("Match() = %q, want the exact regional variant", got)
	}
}

func TestBaseLanguagePreferenceAcceptsARegionalOffering(t *testing.T) {
	got := Match([]string{"ru"}, []string{"en", "ru-KZ"}, "en")

	if got != "ru-KZ" {
		t.Errorf("Match() = %q, want ru-KZ", got)
	}
}

func TestUnmatchedPreferenceFallsBackToTheDefault(t *testing.T) {
	got := Match([]string{"de", "fr"}, []string{"en", "ru"}, "ru")

	if got != "ru" {
		t.Errorf("Match() = %q, want the supplied fallback", got)
	}
}

func TestNoPreferenceFallsBackToTheDefault(t *testing.T) {
	got := Match(nil, []string{"en", "ru"}, "ru")

	if got != "ru" {
		t.Errorf("Match() = %q, want the supplied fallback", got)
	}
}

func TestFallbackOutsideTheAvailableSetYieldsTheFirstAvailable(t *testing.T) {
	got := Match([]string{"de"}, []string{"ro", "ru"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want the first available language", got)
	}
}

func TestNothingAvailableYieldsEmpty(t *testing.T) {
	if got := Match([]string{"en"}, nil, "en"); got != "" {
		t.Errorf("Match() = %q, want empty when nothing is available", got)
	}
}

func TestWildcardTakesTheFallback(t *testing.T) {
	got := Match([]string{"*"}, []string{"en", "ru"}, "ru")

	if got != "ru" {
		t.Errorf("Match() = %q, want the fallback for a wildcard", got)
	}
}

func TestParseAcceptLanguageOrdersByQuality(t *testing.T) {
	got := ParseAcceptLanguage("en;q=0.4, ru;q=0.9, ro")

	// No q means q=1, so ro leads, then ru, then en.
	want := []string{"ro", "ru", "en"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestParseAcceptLanguageKeepsDocumentOrderWithinEqualQuality(t *testing.T) {
	got := ParseAcceptLanguage("ro, ru, en")

	want := []string{"ro", "ru", "en"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestParseAcceptLanguageDropsZeroQuality(t *testing.T) {
	got := ParseAcceptLanguage("ru;q=0, en")

	want := []string{"en"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestParseAcceptLanguageSurvivesGarbage(t *testing.T) {
	for _, header := range []string{
		"",
		";;;",
		"ru;q=abc",
		"ru;q=",
		",,,",
		"ru;;q=0.5",
		"этонеязык",
	} {
		got := ParseAcceptLanguage(header)
		for _, tag := range got {
			if tag == "" {
				t.Errorf("ParseAcceptLanguage(%q) produced an empty tag: %v", header, got)
			}
		}
	}
}

func TestParseAcceptLanguageBoundsTheNumberOfTags(t *testing.T) {
	header := ""
	for range 200 {
		header += "en,"
	}

	if got := ParseAcceptLanguage(header); len(got) > maxAcceptedTags {
		t.Errorf("ParseAcceptLanguage returned %d tags, want at most %d", len(got), maxAcceptedTags)
	}
}
