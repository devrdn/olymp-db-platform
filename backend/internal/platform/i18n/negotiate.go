// Package i18n picks which language to answer a request in.
//
// It knows nothing about contests, users or HTTP: it takes an ordered list of
// what the caller wants, the list of what actually exists, and a fallback, and
// returns one language code. Everything language-shaped in the service — the
// story, the questions, the interface — resolves through this one function, so
// "which language did they get, and why" has a single answer.
//
// The set of languages is data (the `languages` table), never a constant here:
// adding a fourth language is an INSERT, and this code keeps working because
// it only ever compares the codes it is handed.
package i18n

import (
	"strconv"
	"strings"
)

// maxAcceptedTags bounds how much of an Accept-Language header is honoured.
// The header is client-supplied and reaches endpoints that need no
// authentication, so an unbounded list would become an unbounded loop over the
// available languages.
const maxAcceptedTags = 16

// Match returns the best available language for the given preferences.
//
// Preferences are tried in order; for each, an exact match wins, then a match
// on the base language ("ro-MD" accepts "ro", and "ru" accepts "ru-KZ"), since
// a region is a refinement of a language rather than a different one.
//
// When nothing matches, fallback is used if it is actually available, and the
// first available language otherwise — a contest whose declared default was
// later removed must still serve something it has. With nothing available at
// all the result is empty: there is no honest answer, and the caller has to
// treat that as missing content rather than serve a language nobody authored.
func Match(preferred, available []string, fallback string) string {
	if len(available) == 0 {
		return ""
	}

	for _, want := range preferred {
		want = normalize(want)
		if want == "" || want == "*" {
			// "*" means "anything"; the fallback is the most sensible reading.
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

// exact reports an available language equal to want, ignoring case.
func exact(want string, available []string) (string, bool) {
	for _, have := range available {
		if normalize(have) == want {
			return have, true
		}
	}
	return "", false
}

// byBaseLanguage matches on the part before the region subtag, in either
// direction: a request for "ro-MD" accepts an available "ro", and a request
// for "ru" accepts an available "ru-KZ".
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
// most-wanted first.
//
// Tags with q=0 are dropped — that is the client saying "explicitly not this
// one". Anything unparseable is skipped rather than guessed at: the header is
// attacker-controlled, and a malformed tag must never reach a query.
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

	// Highest quality first, preserving document order within a quality — Go's
	// sort.SliceStable would do, but an insertion pass over at most
	// maxAcceptedTags entries avoids pulling in the dependency for a list this
	// small.
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

// qualityOf reads the q-value out of an Accept-Language parameter list.
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
	// Parameters that carry no q at all leave the default weight in place.
	return 1, true
}
