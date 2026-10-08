package sentineltest_test

import (
	"os"
	"path/filepath"
	"slices"
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
