package contests_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

// Every exported sentinel is in Errors() or named here with the reason it
// never reaches a response.
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".",
		// Submit's retry loop absorbs it; when retries run out it returns
		// ErrTooManyAttemptConflicts, which wraps it and has its own row.
		"ErrAttemptConflict",
	)
}
