package sentineltest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const header = "package odd\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\nconst Max = 3\n\n"

// Every exported Err… counts, however it is built — a message wrapped with
// fmt.Errorf around a bound is as much a sentinel as errors.New. What Errors()
// returns is read from its own body, so the two are compared by name.
// Unexported values and test files are not the package's API.
func TestScanReadsEverySentinelAndWhatErrorsReturns(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"odd.go": header +
			"var ErrPlain = errors.New(\"plain\")\n" +
			"var ErrBound = fmt.Errorf(\"at most %d\", Max)\n" +
			"var errHidden = errors.New(\"unexported\")\n\n" +
			"func Errors() []error { return []error{ErrPlain, ErrBound} }\n",
		"odd_test.go": "package odd\n\nimport \"errors\"\n\nvar ErrOnlyInTests = errors.New(\"test\")\n",
	})

	declared, listed, err := sentineltest.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !slices.Equal(declared, []string{"ErrBound", "ErrPlain"}) {
		t.Errorf("declared = %v, want ErrBound and ErrPlain", declared)
	}
	if !slices.Equal(listed, []string{"ErrBound", "ErrPlain"}) {
		t.Errorf("listed = %v, want ErrBound and ErrPlain", listed)
	}
}

// Errors() has to be a plain list of the package's own sentinels; anything
// else cannot be read, and is said so rather than taken as empty.
func TestScanRefusesAnErrorsItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"missing":  "",
		"computed": "func Errors() []error { return append([]error{}, ErrPlain) }\n",
		"foreign":  "func Errors() []error { return []error{other.ErrX} }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writePackage(t, map[string]string{
				"odd.go": header + "var ErrPlain = errors.New(\"plain\")\n\n" + body,
			})
			if _, _, err := sentineltest.Scan(dir); err == nil {
				t.Fatal("Scan accepted an Errors() it cannot read")
			}
		})
	}
}

// A source that does not parse is an error, not an empty package.
func TestScanRefusesSourceItCannotParse(t *testing.T) {
	dir := writePackage(t, map[string]string{"broken.go": "package broken\n\nvar ErrX = \n"})
	if _, _, err := sentineltest.Scan(dir); err == nil {
		t.Fatal("Scan accepted source that does not parse")
	}
}

// recorder stands in for the test AssertListed is given, collecting what it
// reports instead of failing the test that runs it.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// AssertListed reports a sentinel missing from both lists, one named twice,
// and a name that is not one of the package's errors — each by name.
func TestAssertListedNamesEveryMismatch(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"odd.go": header +
			"var ErrListed = errors.New(\"listed\")\n" +
			"var ErrForgotten = errors.New(\"forgotten\")\n" +
			"var ErrTwice = errors.New(\"twice\")\n\n" +
			"func Errors() []error { return []error{ErrListed, ErrTwice} }\n",
	})

	r := &recorder{TB: t}
	sentineltest.AssertListed(r, dir, "ErrTwice", "ErrImagined")

	for _, want := range []string{"ErrForgotten is declared", "ErrTwice is listed more than once", "ErrImagined is listed but not"} {
		if !slices.ContainsFunc(r.errors, func(got string) bool { return strings.Contains(got, want) }) {
			t.Errorf("no report containing %q in %q", want, r.errors)
		}
	}
	if len(r.errors) != 3 {
		t.Errorf("reports = %q, want exactly three", r.errors)
	}
}

// A package whose lists agree is reported clean.
func TestAssertListedPassesWhenEverySentinelIsAccountedFor(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"odd.go": header +
			"var ErrOut = errors.New(\"out\")\n" +
			"var ErrIn = fmt.Errorf(\"at most %d\", Max)\n\n" +
			"func Errors() []error { return []error{ErrOut} }\n",
	})

	r := &recorder{TB: t}
	sentineltest.AssertListed(r, dir, "ErrIn")
	if len(r.errors) != 0 {
		t.Errorf("reports = %q, want none", r.errors)
	}
}
