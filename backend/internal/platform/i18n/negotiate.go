// Package i18n picks which language to answer a request in, from the
// caller's preferences, the languages that exist and a fallback. It knows
// nothing about contests, users or HTTP, and holds no list of languages: they
// are data (the `languages` table), so adding one needs no code change.
package i18n

import (
	"strconv"
	"strings"
)

// maxAcceptedTags bounds how much of an Accept-Language header is honoured:
// the header reaches unauthenticated endpoints, and each tag is a loop over
// the available languages.
const maxAcceptedTags = 16

// Match returns the best available language for the given preferences.
//
// Preferences are tried in order; for each, an exact match wins, then a match
// on the base language ("ro-MD" accepts "ro", "ru" accepts "ru-KZ").
//
// When nothing matches, fallback is used if available, else the first
// available language. With nothing available the result is empty, and the
// caller must treat that as missing content.
func Match(preferred, available []string, fallback string) string {
	if len(available) == 0 {
		return ""
	}

	for _, want := range preferred {
		want = normalize(want)
		if want == "" || want == "*" {
			// "*" means anything: use the fallback.
			break
		}

		if hit, ok := exact(want, available); ok {
			return hit
		}
		if hit, ok := byBaseLanguage(want, available); ok {
			return hit
		}
	}

	if hit, ok := exact(normalize(fallback), available); ok {
		return hit
	}
	return available[0]
}

func exact(want string, available []string) (string, bool) {
	for _, have := range available {
		if normalize(have) == want {
			return have, true
		}
	}
	return "", false
}

// byBaseLanguage matches on the part before the region subtag, in either
// direction.
func byBaseLanguage(want string, available []string) (string, bool) {
	wantBase := baseOf(want)
	for _, have := range available {
		if baseOf(normalize(have)) == wantBase {
			return have, true
		}
	}
	return "", false
}

func baseOf(tag string) string {
	if base, _, found := strings.Cut(tag, "-"); found {
		return base
	}
	return tag
}

func normalize(tag string) string {
	return strings.ToLower(strings.TrimSpace(tag))
}

// ParseAcceptLanguage returns the language tags of an Accept-Language header,
// most-wanted first. Tags with q=0 are dropped. Anything unparseable is
// skipped: the header is attacker-controlled and must never reach a query.
func ParseAcceptLanguage(header string) []string {
	if header == "" {
		return nil
	}

	type weighted struct {
		tag     string
		quality float64
	}

	var parsed []weighted
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(part, ";")
		tag = normalize(tag)
		if tag == "" {
			continue
		}

		quality := 1.0
		if params != "" {
			value, ok := qualityOf(params)
			if !ok {
				continue
			}
			quality = value
		}
		if quality <= 0 {
			continue
		}

		parsed = append(parsed, weighted{tag: tag, quality: quality})
		if len(parsed) == maxAcceptedTags {
			break
		}
	}

	// Highest quality first, keeping header order within a quality.
	tags := make([]string, 0, len(parsed))
	for len(parsed) > 0 {
		best := 0
		for i := 1; i < len(parsed); i++ {
			if parsed[i].quality > parsed[best].quality {
				best = i
			}
		}
		tags = append(tags, parsed[best].tag)
		parsed = append(parsed[:best], parsed[best+1:]...)
	}
	return tags
}

func qualityOf(params string) (float64, bool) {
	for _, param := range strings.Split(params, ";") {
		name, value, found := strings.Cut(param, "=")
		if !found || normalize(name) != "q" {
			continue
		}
		quality, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return quality, true
	}
	return 1, true
}
