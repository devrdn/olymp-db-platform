package sqlpolicy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
)

// Codes() is walked by the HTTP layer's own test, so it has to be the whole
// list rather than most of it. Read off the source, because a constant added
// without being listed is exactly the omission that matters.
func TestEveryRefusalCodeIsListed(t *testing.T) {
	source := readSource(t, "refusal.go")

	listed := map[sqlpolicy.Code]bool{}
	for _, code := range sqlpolicy.Codes() {
		listed[code] = true
	}

	for _, line := range strings.Split(source, "\n") {
		name, _, ok := strings.Cut(strings.TrimSpace(line), " Code = ")
		if !ok || !strings.HasPrefix(name, "Code") {
			continue
		}
		value := strings.Trim(strings.TrimSpace(strings.SplitN(line, "Code = ", 2)[1]), `"`)
		if !listed[sqlpolicy.Code(value)] {
			t.Errorf("%s (%q) is declared but not in Codes()", name, value)
		}
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
