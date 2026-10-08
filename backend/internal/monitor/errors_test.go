package monitor_test

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
		// An event from a browser that Normalize refuses is dropped by
		// CleanBatch rather than refusing the batch; every other event the
		// store receives the server builds itself (the tracker, the
		// workspace), so none of these reaches a response.
		"ErrEventInvalid", "ErrAwayTooShort", "ErrPasteTarget",
		// Raised only by a revision the workspace builds from fields it has
		// already bounded; a failure there is a defect of ours and answers as
		// the internal error it is.
		"ErrRevisionInvalid",
		// Found while a contest-wide download is already streaming: it is
		// told as the last line of the file, not as an error response.
		"ErrExportTooWide",
	)
}
