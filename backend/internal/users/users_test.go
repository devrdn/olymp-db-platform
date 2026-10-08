package users_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

// Every sentinel the package exports is in Errors(), which internal/api walks
// to be sure each has an answer, or is named here as one that never leaves it.
func TestEveryExportedErrorIsListed(t *testing.T) {
	// Nothing is internal today: every exported sentinel is a refusal a
	// caller can meet. errUnhandledSkipReason is unexported, and so outside
	// the scan.
	sentineltest.AssertListed(t, ".")
}
