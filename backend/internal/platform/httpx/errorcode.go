package httpx

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Code is the machine-readable half of an error response.
//
// It is a struct rather than a string on purpose: a string literal cannot be
// passed where one of these is expected, so every code that reaches a client
// has necessarily been declared through NewCode and is necessarily in the
// catalog. That is what lets the contract be generated exactly instead of
// recovered by pattern-matching the source — which is how `not_publishable`
// once shipped with no message in any language while the check that existed to
// prevent exactly that reported success.
//
// Clients switch on the code; the message beside it is for whoever is reading
// a log or writing the client, never for a user. Translating is the interface's
// job (see docs/ARCHITECTURE.md §6.2), which is why the server never needs to
// know what language anybody reads.
type Code struct {
	value string
}

// String returns the wire value.
func (c Code) String() string { return c.value }

// CodeInfo is one entry of the published catalog.
type CodeInfo struct {
	Code string `json:"code"`
	// Meaning says when the code arrives. It documents the contract for the
	// people writing clients; it is not a message to show anybody.
	Meaning string `json:"meaning"`
}

// codeValue is the shape a code has to take. It travels to clients that switch
// on it and into a generated file that is committed, so anything but
// lower_snake_case is a typo rather than a style choice.
var codeValue = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

var (
	catalogMu sync.RWMutex
	catalog   = map[string]string{}
)

// NewCode declares a code and its meaning, and returns the value to answer
// with.
//
// Declaring the same code twice with the same meaning is normal and returns
// the same code: "not found" is both the router's 404 and a handler's missing
// contest, and they are one concept. Two different meanings for one code would
// make the published contract a document that cannot be true, so that is a
// panic — at startup, where it is somebody's own change, rather than in
// whichever client read the file first.
func NewCode(value, meaning string) Code {
	if !codeValue.MatchString(value) {
		panic(fmt.Sprintf("httpx: %q is not a valid error code (lower_snake_case)", value))
	}
	if strings.TrimSpace(meaning) == "" {
		panic(fmt.Sprintf("httpx: error code %q was declared without a meaning", value))
	}

	catalogMu.Lock()
	defer catalogMu.Unlock()

	if existing, declared := catalog[value]; declared && existing != meaning {
		panic(fmt.Sprintf("httpx: error code %q is already declared as %q, cannot redeclare as %q",
			value, existing, meaning))
	}
	catalog[value] = meaning

	return Code{value: value}
}

// Catalog returns every declared code, sorted.
//
// Sorted because it is generated into a file that is committed and reviewed:
// an unstable order would produce a diff on every build and bury the real
// change.
func Catalog() []CodeInfo {
	catalogMu.RLock()
	defer catalogMu.RUnlock()

	entries := make([]CodeInfo, 0, len(catalog))
	for code, meaning := range catalog {
		entries = append(entries, CodeInfo{Code: code, Meaning: meaning})
	}
	slices.SortFunc(entries, func(a, b CodeInfo) int { return strings.Compare(a.Code, b.Code) })
	return entries
}
