package monitor

import (
	"hash/fnv"
	"strings"
	"unicode/utf8"
)

// Fingerprint is a stable 64-bit hash of a statement with case and spacing
// normalised away: the whole text lowercased, literals included, and every
// whitespace run collapsed to one space. Statements differing in one token
// do not match.
//
// It is FNV-1a 64 stored as a signed bigint and compared across releases, so
// changing the normalisation or the hash makes old rows incomparable with
// new ones.
func Fingerprint(sql string) int64 {
	fingerprint, _ := normalised(sql)
	return fingerprint
}

// ComparableFingerprint is the fingerprint the journal stores: Fingerprint
// when the normalised text is at least IdenticalQueryMinChars long, nil
// otherwise, so short common queries never match.
func ComparableFingerprint(sql string) *int64 {
	fingerprint, length := normalised(sql)
	if length < IdenticalQueryMinChars {
		return nil
	}
	return &fingerprint
}

// normalised hashes the normalised text and counts its characters in one
// pass.
func normalised(sql string) (fingerprint int64, length int) {
	hash := fnv.New64a()
	// Written field by field, so no normalised copy is built.
	for i, field := range strings.Fields(sql) {
		if i > 0 {
			_, _ = hash.Write([]byte{' '})
			length++
		}
		lower := strings.ToLower(field)
		_, _ = hash.Write([]byte(lower))
		length += utf8.RuneCountInString(lower)
	}
	// A bit-for-bit reinterpretation, not a numeric conversion.
	return int64(hash.Sum64()), length // #nosec G115 -- the bit pattern is the value; the sign carries no meaning.
}
