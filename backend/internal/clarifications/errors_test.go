package clarifications_test

import (
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/platform/sentineltest"
)

func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}
