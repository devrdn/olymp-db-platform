package monitor

import (
	"regexp"
	"strings"
)

// StaffErrorText is what a contest's staff are shown of a journalled query's
// error.
//
// More than the participant's own log shows (api.participantSafeError keeps
// only the validator's refusals), because what an organiser needs to judge is
// the participant's SQL: PostgreSQL's words about their statement — "column
// does not exist", "division by zero" — and the policy's refusals are the
// feedback the participant worked from. Less than everything, because contest
// staff are organisers, not the platform's operators: when the failure was
// ours — the query service unreachable, a connection to the game cluster
// refused or failed to authenticate, the server shutting down — the recorded
// text names hosts, ports, roles and database names that are the
// installation's business, and it says nothing about the participant.
//
// The journal keeps only the text, so the rule reads the text: a refusal or a
// timeout is shown whole; an error is shown only when it carries PostgreSQL's
// own SQLSTATE of a class about a statement, and nothing marking a
// connection. Everything else is withheld, as it is from the participant.
func StaffErrorText(status, text string) string {
	switch status {
	case "rejected", "timeout":
		return text
	case "error":
		if statementError(text) {
			return text
		}
	}
	return ""
}

// sqlState finds PostgreSQL's code in pgx's rendering of its error.
var sqlState = regexp.MustCompile(`\(SQLSTATE ([0-9A-Z]{5})\)`)

// infrastructureClasses are the SQLSTATE classes about the server rather
// than the statement: connection exceptions (08), invalid authorization
// (28), invalid catalog name (3D), operator intervention such as a shutdown
// (57), and system errors (58).
var infrastructureClasses = map[string]bool{"08": true, "28": true, "3D": true, "57": true, "58": true}

// connectionMarkers are what a failure to reach a server leaves in its text.
var connectionMarkers = []string{"failed to connect", "dial tcp", "host=", "user=", "database=",
	"connection refused", "could not answer", "server closed the connection", "no such host"}

func statementError(text string) bool {
	match := sqlState.FindStringSubmatch(text)
	if match == nil || infrastructureClasses[match[1][:2]] {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range connectionMarkers {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}
