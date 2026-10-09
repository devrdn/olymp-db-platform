package postgres

import "fmt"

// publicStatusFilter keeps drafts away from anonymous readers. The caller
// binds contests.PublicStatuses at the given placeholder, so the profile,
// showcase and covers queries share one list of statuses. column is passed in
// because the statements alias the contests table differently.
func publicStatusFilter(column string, placeholder int) string {
	return fmt.Sprintf("%s = ANY($%d)", column, placeholder)
}
