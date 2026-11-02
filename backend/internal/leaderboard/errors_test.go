package leaderboard_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

// Every exported sentinel is in Errors(): the table, the staff, the reveal
// and the results routes all answer them.
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".")
}
