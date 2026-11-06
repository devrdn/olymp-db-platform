package registrations_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	// The store's two refusals are resolved by the service and never reach a
	// caller.
	sentineltest.AssertListed(t, ".", "ErrAlreadyRegistered", "ErrCodeTaken")
}
