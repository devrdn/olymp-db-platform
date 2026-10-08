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

// A plain sentinel is read with its message; one declared any other way is
// named rather than skipped, so a completeness check cannot pass without
// looking at it. Test files are not the package's API and are not read.
func TestScanReadsPlainSentinelsAndNamesTheRest(t *testing.T) {
	dir := writePackage(t, map[string]string{
		"odd.go": "package odd\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n" +
			"var ErrPlain = errors.New(\"plain\")\n" +
			"var errHidden = errors.New(\"unexported\")\n" +
			"var ErrWrapped = fmt.Errorf(\"%w: more\", ErrPlain)\n" +
			"var ErrA, ErrB = pair()\n\n" +
			"func pair() (error, error) { return ErrPlain, errHidden }\n",
		"odd_test.go": "package odd\n\nimport \"errors\"\n\nvar ErrOnlyInTests = errors.New(\"test\")\n",
	})

	declared, unclassified, err := sentineltest.Scan(dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(declared) != 1 || declared["ErrPlain"] != "plain" {
		t.Errorf("declared = %v, want only ErrPlain", declared)
	}
	slices.Sort(unclassified)
	if !slices.Equal(unclassified, []string{"ErrA", "ErrB", "ErrWrapped"}) {
		t.Errorf("unclassified = %v, want ErrA, ErrB and ErrWrapped", unclassified)
	}
}

// A source that does not parse is an error, not an empty package.
func TestScanRefusesSourceItCannotParse(t *testing.T) {
	dir := writePackage(t, map[string]string{"broken.go": "package broken\n\nvar ErrX = \n"})
	if _, _, err := sentineltest.Scan(dir); err == nil {
		t.Fatal("Scan accepted source that does not parse")
	}
}
