package postgres

import "fmt"

// publicStatusFilter is the predicate that keeps a draft away from anybody
// without a session, written against the placeholder the caller has free.
//
// The list of statuses is not here: it is `contests.PublicStatuses`, and it is
// bound as a parameter rather than spelled into the SQL. Three repositories —
// the profile, the showcase and the covers — each wrote the same four statuses
// out by hand, with a comment in one of them admitting that a change to one
// "has to find the other". Nothing made it. A status added to two of the three
// is a contest shown to a stranger who should not have seen it, and the
// mistake is invisible until somebody reads all three files at once.
//
// The column is named by the caller because the three statements alias the
// contests table differently.
func publicStatusFilter(column string, placeholder int) string {
	return fmt.Sprintf("%s = ANY($%d)", column, placeholder)
}
