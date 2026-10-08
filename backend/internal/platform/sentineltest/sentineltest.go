// Package sentineltest checks that a package lists every error it declares.
//
// A domain package hands its errors to the HTTP layer through a list of its
// own (queryproxy.Errors(), for one), and internal/api answers each from a
// table. A sentinel declared and left off that list reaches a client as
// "internal error" for a refusal that is really theirs (CLAUDE.md, security
// rule 1). A list kept by hand is exactly what drifts, so this reads the
// package's source and compares names: every exported Err… the package
// declares, however it is built, against the names its Errors() returns.
//
// It reads source and nothing else: no database, no network. It does not
// decide what an error means to a client; that is internal/api's table.
package sentineltest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// AssertListed fails t unless every exported Err… declared in the package at
// dir is returned by its Errors() or named in internal (those that never reach
// a caller), exactly once, and nothing else is.
func AssertListed(t testing.TB, dir string, internal ...string) {
	t.Helper()
	declared, listed, err := Scan(dir)
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
	}

	accounted := map[string]int{}
	for _, name := range append(listed, internal...) {
		accounted[name]++
	}
	for _, name := range declared {
		switch accounted[name] {
		case 0:
			t.Errorf("%s is declared but neither in Errors() nor internal", name)
		case 1:
		default:
			t.Errorf("%s is listed more than once", name)
		}
		delete(accounted, name)
	}
	for name := range accounted {
		t.Errorf("%s is listed but not an exported error of the package", name)
	}
}

// Scan reads the non-test Go source in dir and returns, sorted, the names of
// every exported package-level Err… it declares and the names its Errors()
// returns. Errors() must be a single return of a []error literal naming the
// package's own sentinels; anything else is an error rather than an empty
// list, so a check cannot pass without reading it.
func Scan(dir string) (declared, listed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	fset := token.NewFileSet()
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", name, err)
		}
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				if decl.Tok != token.VAR {
					continue
				}
				for _, spec := range decl.Specs {
					for _, ident := range spec.(*ast.ValueSpec).Names {
						if ident.IsExported() && strings.HasPrefix(ident.Name, "Err") {
							declared = append(declared, ident.Name)
						}
					}
				}
			case *ast.FuncDecl:
				if decl.Recv != nil || decl.Name.Name != "Errors" {
					continue
				}
				names, err := returnedNames(decl)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: Errors(): %w", name, err)
				}
				listed, found = names, true
			}
		}
	}
	if !found {
		return nil, nil, errors.New("the package has no Errors() function")
	}
	slices.Sort(declared)
	slices.Sort(listed)
	return declared, listed, nil
}

// returnedNames reads `return []error{ErrA, ErrB}` out of fn's body.
func returnedNames(fn *ast.FuncDecl) ([]string, error) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return nil, errors.New("its body is not a single return")
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return nil, errors.New("its body is not a single return")
	}
	lit, ok := ret.Results[0].(*ast.CompositeLit)
	if !ok {
		return nil, errors.New("it does not return a []error literal")
	}
	names := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		ident, ok := elt.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("%s is not one of the package's own sentinels", describe(elt))
		}
		names = append(names, ident.Name)
	}
	return names, nil
}

// describe names an expression for an error message.
func describe(expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok {
			return pkg.Name + "." + sel.Sel.Name
		}
	}
	return fmt.Sprintf("%T", expr)
}
