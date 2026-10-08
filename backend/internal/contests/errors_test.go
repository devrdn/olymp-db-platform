package contests_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

// Errors is how the HTTP layer learns which of this package's errors reach a
// caller, and so which ones need an answer of their own (internal/api's
// errorTable). Every exported sentinel in the package's source is on it or
// named here, with the reason it never reaches a response.
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".",
		// The store reports a lost attempt-number race with it and Submit's
		// retry loop absorbs it, the only caller of the write that returns
		// it. When the retries run out Submit returns
		// ErrTooManyAttemptConflicts, which has a row of its own and wraps
		// this one as its cause; the row is found by the outer sentinel.
		"ErrAttemptConflict",
	)
}
