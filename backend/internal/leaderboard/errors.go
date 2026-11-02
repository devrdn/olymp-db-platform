package leaderboard

// Errors is every error this package hands to a caller that answers a
// request with it (CLAUDE.md rule 1).
func Errors() []error {
	return []error{
		ErrNotFound, ErrNotAParticipant, ErrNotRevealable,
		ErrInvalidReport, ErrUnknownRegistration, ErrEmailForbidden, ErrTooManyRows,
	}
}
