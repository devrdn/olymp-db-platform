// Package sentineltest checks that a package lists every error it declares.
//
// A domain package hands its errors to the HTTP layer through a list of its
// own (queryproxy.Errors(), for one), and internal/api answers each from a
// table. A sentinel declared and left off that list reaches a client as
// "internal error" for a refusal that is really theirs (CLAUDE.md, security
// rule 1). A list kept by hand is exactly what drifts, so this reads the
// package's source and compares.
//
// It reads source and nothing else: no database, no network. It does not
// decide what an error means to a client; that is internal/api's table.
package sentineltest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// AssertListed fails t unless every exported `Err… = errors.New("…")` in the
// package at dir is in listed (what the package hands to callers) or internal
// (what never leaves it), each exactly once, and unless every exported Err…
// is declared that plainly — one built any other way cannot be matched by its
// message, and is named rather than skipped.
func AssertListed(t testing.TB, dir string, listed, internal []error) {
	t.Helper()
	declared, unclassified, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
	}
	if len(declared) == 0 && len(unclassified) == 0 {
		t.Fatalf("found no exported errors in %s; the scan is looking in the wrong place", dir)
	}
	for _, name := range unclassified {
		t.Errorf("%s is exported but not a plain errors.New sentinel, so it cannot be checked against the list", name)
	}

	known := map[string]int{}
	for _, err := range append(append([]error(nil), listed...), internal...) {
		known[err.Error()]++
	}
	for name, message := range declared {
		switch known[message] {
		case 0:
			t.Errorf("%s (%q) is declared but neither listed nor internal", name, message)
		case 1:
		default:
			t.Errorf("%s (%q) is listed more than once", name, message)
		}
	}
	if len(listed)+len(internal) != len(declared) {
		t.Errorf("the lists hold %d errors, the package declares %d", len(listed)+len(internal), len(declared))
	}
}

// Scan reads the non-test Go source in dir and returns every exported
// `Err… = errors.New("…")` by name with its message, and, apart, the names of
// exported Err… values declared any other way.
func Scan(dir string) (declared map[string]string, unclassified []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	declared = map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, ident := range spec.Names {
				if !ident.IsExported() || !strings.HasPrefix(ident.Name, "Err") {
					continue
				}
				if message, ok := plainSentinel(spec, i); ok {
					declared[ident.Name] = message
				} else {
					unclassified = append(unclassified, ident.Name)
				}
			}
			return true
		})
	}
	return declared, unclassified, nil
}

// plainSentinel reports the message of the i-th name in spec when its value is
// exactly errors.New("…").
func plainSentinel(spec *ast.ValueSpec, i int) (string, bool) {
	if len(spec.Values) != len(spec.Names) {
		return "", false
	}
	call, ok := spec.Values[i].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || fun.Sel.Name != "New" {
		return "", false
	}
	if pkg, ok := fun.X.(*ast.Ident); !ok || pkg.Name != "errors" {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	message, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return message, true
}
