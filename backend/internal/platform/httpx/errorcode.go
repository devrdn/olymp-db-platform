package httpx

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Code is the machine-readable half of an error response. It is a struct so a
// string literal cannot be passed: every code a client sees was declared
// through NewCode and is in the generated catalog. Clients switch on it and
// translate it; the server never needs the reader's language.
type Code struct {
	value string
}

func (c Code) String() string { return c.value }

type CodeInfo struct {
	Code string `json:"code"`
	// Meaning documents when the code arrives, for client authors.
	Meaning string `json:"meaning"`
}

// codeValue is the shape a code has to take: lower_snake_case.
var codeValue = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

var (
	catalogMu sync.RWMutex
	catalog   = map[string]string{}
)

// NewCode declares a code and its meaning, and returns the value to answer
// with. Declaring the same code twice with the same meaning returns the same
// code; a different meaning panics at startup.
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

// Catalog returns every declared code, sorted so the generated file is stable.
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
