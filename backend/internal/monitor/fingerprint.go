package monitor

import (
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

// Fingerprint is a stable 64-bit hash of a statement's text with case and
// spacing normalised away, the key "the same query as another participant"
// (design §5) is counted on.
//
// Normalising is lowercasing the whole text and collapsing every run of
// whitespace to a single space, with none at either end. It is deliberately
// that and nothing more: two statements that differ only in how they were
// typed out compare equal, and two that differ in a single token do not —
// including inside a string literal, which is lowercased along with the rest,
// because a copied query is copied literals and all.
//
// The hash is FNV-1a 64, reinterpreted as a signed integer so it fits a
// bigint column. The value is stored and compared across processes and
// releases, so it must depend on nothing but the text: changing the
// normalisation or the hash leaves every row written before the change
// incomparable with every row written after.
//
// Computed once, in Go, by the insert that journals the query
// (postgres.QueryLog.Begin), not by the database.
func Fingerprint(sql string) int64 {
	hash := fnv.New64a()
	// strings.Fields splits on every run of Unicode whitespace and drops the
	// ends; writing the fields back with one space between them is the
	// collapse. Written piece by piece so no normalised copy is built.
	for i, field := range strings.Fields(sql) {
		if i > 0 {
			_, _ = hash.Write([]byte{' '})
		}
		_, _ = hash.Write([]byte(strings.ToLower(field)))
	}
	// Two's-complement reinterpretation of the unsigned sum, not a numeric
	// conversion: every bit is kept.
	return int64(hash.Sum64()) // #nosec G115 -- the bit pattern is the value; the sign carries no meaning.
}

// ComparableFingerprint is the fingerprint the journal stores: the
// statement's Fingerprint when its normalised text is at least
// IdenticalQueryMinChars characters long, and nil otherwise.
//
// Only a statement that long can raise "the same query as another
// participant" (design §5) — what everybody types is not a sign of copying —
// so a shorter one is stored without a fingerprint, and the participants
// table compares fingerprints without measuring any text. The normalisation
// is this file's alone; nothing in SQL repeats it.
func ComparableFingerprint(sql string) *int64 {
	length := 0
	for i, field := range strings.Fields(sql) {
		if i > 0 {
			length++
		}
		length += utf8.RuneCountInString(strings.ToLower(field))
	}
	if length < IdenticalQueryMinChars {
		return nil
	}
	fingerprint := Fingerprint(sql)
	return &fingerprint
}
