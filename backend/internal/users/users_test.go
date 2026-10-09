package users_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	// Every exported sentinel reaches callers; errUnhandledSkipReason is
	// unexported.
	sentineltest.AssertListed(t, ".")
}
