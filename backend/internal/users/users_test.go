package users_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
	"github.com/devrdn/db-contest/backend/internal/users"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	// Every exported sentinel reaches callers; errUnhandledSkipReason is
	// unexported.
	sentineltest.AssertListed(t, ".")
}

// CLAUDE.md rule 2: the query runs through trigram ILIKEs on every listing.
// The bound counts characters, as pg_trgm does, so a Cyrillic query at the
// limit passes although it is twice as many bytes.
func TestFilterValidateBoundsTheQueryInCharacters(t *testing.T) {
	for name, query := range map[string]string{
		"empty":                  "",
		"at the limit":           strings.Repeat("a", users.MaxQueryLength),
		"at the limit, Cyrillic": strings.Repeat("ф", users.MaxQueryLength),
	} {
		if err := (users.Filter{Query: query}).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want no error", name, err)
		}
	}
	for name, query := range map[string]string{
		"over the limit":           strings.Repeat("a", users.MaxQueryLength+1),
		"over the limit, Cyrillic": strings.Repeat("ф", users.MaxQueryLength+1),
	} {
		if err := (users.Filter{Query: query}).Validate(); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("%s: Validate() = %v, want ErrInvalidAccount", name, err)
		}
	}
}
