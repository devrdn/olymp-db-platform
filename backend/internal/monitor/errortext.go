package monitor

import (
	"regexp"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/queryrunner"
)

// StaffErrorText is what a contest's staff are shown of a journalled query's
// error: PostgreSQL's words about the participant's statement, but not
// infrastructure failures, whose text names hosts, ports, roles and databases.
//
// The journal keeps only text, so the decision is made from the text: a
// refusal or timeout is shown whole; an error is shown when it is a runner
// verdict about the query or carries a statement-class SQLSTATE, and in either
// case only with no connection marker. Everything else is withheld. An error
// from preparing the session (its role or limits) has a statement-class
// SQLSTATE and is shown; it names nothing of the installation's.
func StaffErrorText(status, text string) string {
	switch status {
	case "rejected", "timeout":
		return text
	case "error":
		if (runnerVerdict(text) || statementError(text)) && !mentionsConnection(text) {
			return text
		}
	}
	return ""
}

var sqlState = regexp.MustCompile(`\(SQLSTATE ([0-9A-Z]{5})\)`)

// infrastructureClasses are the SQLSTATE classes about the server rather
// than the statement: connection exceptions (08), invalid authorization
// (28), invalid catalog name (3D), operator intervention such as a shutdown
// (57), and system errors (58).
var infrastructureClasses = map[string]bool{"08": true, "28": true, "3D": true, "57": true, "58": true}

var connectionMarkers = []string{"failed to connect", "dial tcp", "host=", "user=", "database=",
	"connection refused", "could not answer", "server closed the connection", "no such host"}

// runnerVerdicts are the runner's own errors about a query hitting a limit.
var runnerVerdicts = []error{queryrunner.ErrResultTooLarge, queryrunner.ErrCanceled, queryrunner.ErrTimeout}

func runnerVerdict(text string) bool {
	for _, verdict := range runnerVerdicts {
		if text == verdict.Error() || strings.HasSuffix(text, ": "+verdict.Error()) {
			return true
		}
	}
	return false
}

func statementError(text string) bool {
	match := sqlState.FindStringSubmatch(text)
	return match != nil && !infrastructureClasses[match[1][:2]]
}

func mentionsConnection(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range connectionMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
