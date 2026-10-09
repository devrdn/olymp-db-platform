package monitor

// Errors is every error this package hands to a caller that answers a
// request with it (CLAUDE.md rule 1). Errors that never reach a response are
// named, with the reason, in TestEveryExportedErrorIsListed.
func Errors() []error {
	return []error{
		ErrParticipantNotFound, ErrRevisionNotFound, ErrInvalidCursor,
		ErrInvalidFeedFilter, ErrInvalidQueryFilter,
		ErrSignalsTooOften, ErrBatchTooLarge, ErrTooManyEvents,
	}
}
