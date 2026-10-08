package contests

// Errors is every error this package hands to a caller that has to answer a
// request with it: the organiser's refusals (a contest, question, story,
// participant or staff member that is not there; a change the contest's status
// or schedule does not allow; a roster, a search or a package out of bounds),
// the enrolment refusals, what a participant meets at the participation gate
// (StandingOf), and what a participant meets submitting an answer.
// internal/api answers each from a table of its own, and a test there walks
// this list so that none can reach a client as "internal error"; a test here
// reads the package's source so that none can be declared and left off it.
//
// ErrNotPublishable is on the list although the publish gate returns it as a
// *NotPublishableError: the handler answers that with the list of problems
// attached, and the table holds the answer for the sentinel itself.
//
// Not everything is here. ErrAttemptConflict is declared beside these and
// named in TestEveryExportedErrorIsListed, with the reason.
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
		ErrNotAParticipant, ErrContestNotRunning, ErrParticipantFinished, ErrDeadlinePassed,
		ErrInvalidContest, ErrInvalidQuestion, ErrInvalidAnswer,
		ErrInvalidPolicy, ErrInvalidRole, ErrUnknownLanguage,
		ErrRosterTooLarge, ErrQueryTooLong,
		ErrAnswerTooLong, ErrNotAChoice, ErrQuestionClosed,
		ErrQuestionNotOpen, ErrTooManyAttemptConflicts,
	}
}
