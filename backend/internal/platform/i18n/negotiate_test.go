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
	// The caller's first choice that the contest actually offers wins, not the
	// first offered language that happens to appear in the list.
	got := Match([]string{"kk", "ru", "en"}, []string{"en", "ru"}, "en")

	if got != "ru" {
		t.Errorf("Match() = %q, want ru — kk is unavailable, ru is the next preference", got)
	}
}

func TestMatchIsCaseInsensitive(t *testing.T) {
	// Accept-Language is case-insensitive per RFC 9110; browsers send "RO" and
	// "ro-RO" interchangeably.
	got := Match([]string{"RO"}, []string{"en", "ro"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want ro", got)
	}
}

func TestRegionalPreferenceFallsBackToItsBaseLanguage(t *testing.T) {
	// A Moldovan browser asking for ro-MD should get the Romanian content
	// rather than the fallback: the region is a refinement, not a different
	// language.
	got := Match([]string{"ro-MD"}, []string{"en", "ro", "ru"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want ro", got)
	}
}

func TestExactRegionalMatchBeatsBaseLanguage(t *testing.T) {
	// If the contest went to the trouble of authoring ru-KZ, someone asking
	// for ru-KZ gets it rather than plain ru.
	got := Match([]string{"ru-KZ"}, []string{"ru", "ru-KZ"}, "en")

	if got != "ru-KZ" {
		t.Errorf("Match() = %q, want the exact regional variant", got)
	}
}

func TestBaseLanguagePreferenceAcceptsARegionalOffering(t *testing.T) {
	// Asking for "ru" when only ru-KZ is authored still beats the fallback.
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
	// A contest whose declared default was later removed must still serve
	// something rather than a language it does not have.
	got := Match([]string{"de"}, []string{"ro", "ru"}, "en")

	if got != "ro" {
		t.Errorf("Match() = %q, want the first available language", got)
	}
}

func TestNothingAvailableYieldsEmpty(t *testing.T) {
	// There is no honest answer here; the caller has to treat it as "no
	// content", not silently serve a language nobody authored.
	if got := Match([]string{"en"}, nil, "en"); got != "" {
		t.Errorf("Match() = %q, want empty when nothing is available", got)
	}
}

func TestWildcardTakesTheFallback(t *testing.T) {
	// "Accept-Language: *" means "anything"; the contest's own default is the
	// most sensible reading of that.
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
	// q=0 means "explicitly not this one".
	got := ParseAcceptLanguage("ru;q=0, en")

	want := []string{"en"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestParseAcceptLanguageSurvivesGarbage(t *testing.T) {
	// The header is attacker-controlled; it must never panic and must never
	// yield nonsense that reaches a database query.
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
	// An unbounded header would otherwise turn into an unbounded loop over
	// available languages on an endpoint reachable before authentication.
	header := ""
	for range 200 {
		header += "en,"
	}

	if got := ParseAcceptLanguage(header); len(got) > maxAcceptedTags {
		t.Errorf("ParseAcceptLanguage returned %d tags, want at most %d", len(got), maxAcceptedTags)
	}
}
