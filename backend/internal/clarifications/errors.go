package clarifications

// Errors lists every sentinel this package declares, so internal/api can
// answer each one (CLAUDE.md rule 1).
func Errors() []error {
	return []error{
		ErrQuestionTooLong, ErrAnswerTooLong, ErrAnnouncementTooLong, ErrEmpty, ErrTextInvalid,
		ErrTooManyOpen, ErrTooOften, ErrNotFound, ErrQuestionNotFound, ErrAlreadyAnswered,
		ErrNotAnswered, ErrInvalidVisibility, ErrClosed, ErrContestNotFound, ErrInvalidCursor,
	}
}
