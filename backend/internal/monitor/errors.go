package monitor

// Errors is every error this package hands to a caller that has to answer a
// request with it: the refusals of the organiser's reads (a participant or a
// revision that is not there, a cursor or a filter that cannot be served) and
// of a participant's signals (a batch too often, too large, or past what a
// registration stores). internal/api answers each from a table of its own,
// and a test there walks this list so that none can reach a client as
// "internal error"; a test here reads the package's source so that none can
// be declared and left off it.
//
// Not everything is here. The errors that never reach a response are declared
// beside these and named in TestEveryExportedErrorIsListed, each with the
// reason: an event or a revision the package refuses is dropped or built by
// our own code rather than sent by a caller, and a contest too wide to export
// is told in the last line of a download that has already begun.
func Errors() []error {
	return []error{
		ErrParticipantNotFound, ErrRevisionNotFound, ErrInvalidCursor,
		ErrInvalidFeedFilter, ErrInvalidQueryFilter,
		ErrSignalsTooOften, ErrBatchTooLarge, ErrTooManyEvents,
	}
}
