package contests

// Errors is every error this package hands to the HTTP layer (CLAUDE.md
// rule 1). internal/api answers each from its error table; tests keep the
// source, this list and the table in step.
//
// ErrNotPublishable arrives wrapped in *NotPublishableError; the table answers
// the sentinel. ErrAttemptConflict is internal and left off, as
// TestEveryExportedErrorIsListed records.
func Errors() []error {
	return []error{
		ErrNotFound, ErrQuestionNotFound, ErrStoryNotFound,
		ErrParticipantNotFound, ErrManagerNotFound,
		ErrPackageTooLarge, ErrNotPublishable,
		ErrInvalidTransition, ErrStatusChanged, ErrNotEditable,
		ErrFreezeAlreadyReached, ErrICPCStartLocked, ErrOwnerImmutable,
		ErrAlreadyEnrolled, ErrEnrollmentClosed, ErrParticipantStarted,
		ErrStaffCannotParticipate, ErrParticipantCannotBeStaff,
		ErrAddressNotAllowed,
		ErrNotAParticipant, ErrContestNotRunning, ErrContestEnded, ErrParticipantFinished, ErrDeadlinePassed,
		ErrInvalidContest, ErrInvalidQuestion, ErrInvalidAnswer,
		ErrInvalidPolicy, ErrInvalidRole, ErrUnknownLanguage,
		ErrRosterTooLarge, ErrQueryTooLong,
		ErrAnswerTooLong, ErrNotAChoice, ErrQuestionClosed,
		ErrQuestionNotOpen, ErrTooManyAttemptConflicts,
	}
}
