package registrations_test

import (
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/sentineltest"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	// The store's two refusals are resolved by the service and never reach a
	// caller.
	sentineltest.AssertListed(t, ".", "ErrAlreadyRegistered", "ErrCodeTaken")
}
