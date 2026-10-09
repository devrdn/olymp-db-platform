package monitor_test

import (
	"testing"

	"github.com/devrdn/db-contest/backend/internal/platform/sentineltest"
)

// Every exported sentinel is in Errors() or named here with the reason it
// never reaches a response.
func TestEveryExportedErrorIsListed(t *testing.T) {
	sentineltest.AssertListed(t, ".",
		// A refused browser event is dropped by CleanBatch; other events
		// are built by the server.
		"ErrEventInvalid", "ErrAwayTooShort", "ErrPasteTarget",
		// Only from revisions the workspace builds; a failure is our defect.
		"ErrRevisionInvalid",
		// Reported as the last line of an already-streaming download.
		"ErrExportTooWide",
	)
}
