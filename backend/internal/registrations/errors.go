package registrations

// Errors lists every sentinel this package hands a caller, so internal/api can
// answer each one (CLAUDE.md rule 1). ErrAlreadyRegistered and ErrCodeTaken
// are the store's, and the service resolves both.
func Errors() []error {
	return []error{ErrInvalidName, ErrInvalidEmail, ErrFull, ErrNotFound}
}
