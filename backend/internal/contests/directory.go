package contests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxDirectoryQueryLength bounds the picker search text, in runes (CLAUDE.md
// rule 2).
const MaxDirectoryQueryLength = 100

// MinDirectoryQueryLength is the shortest text SearchPeople sends to the
// database; shorter text returns nothing. It is 3 because a shorter pattern
// yields no pg_trgm trigram, so the trigram indexes (migration 000016) cannot
// serve it and the users table is scanned on every keystroke
// (TestSearchPredicateUsesTheTrigramIndexes). It is not an enumeration
// defence.
const MinDirectoryQueryLength = 3

// DirectorySearchDefaultLimit and DirectorySearchMaxLimit bound a picker's
// results, lower than the admin listing because every contest's staff can
// search.
const (
	DirectorySearchDefaultLimit = 10
	DirectorySearchMaxLimit     = 20
)

// ErrQueryTooLong refuses search text over MaxDirectoryQueryLength.
var ErrQueryTooLong = errors.New("search text is too long")

// Person is a search result for appointing or enrolling somebody: a narrow
// view of users.User with no status, roles or password state.
//
// The search runs behind participant.manage, not users.manage, so any
// contest's staff can call it against every account in the installation.
// Email is therefore the full address only when the query matched the email
// itself, which tells two same-named people apart without handing a
// throwaway contest's owner every address: for a match on the login or the
// name it is masked to the local part's first character, "…" and the domain.
// An account without an email has Email == "".
type Person struct {
	UserID   uuid.UUID
	Login    string
	FullName string
	Email    string
}

// SearchPeople finds accounts by a few typed characters, for the staff and
// participant pickers. It searches the whole installation, not one contest;
// the HTTP layer's contest-scoped participant.manage check is what limits who
// may call it (see Person for what that exposes).
func (s *Service) SearchPeople(ctx context.Context, query string, limit int) ([]Person, error) {
	query = strings.TrimSpace(query)
	// Runes, not bytes: the message says characters, and trigrams are cut on
	// characters.
	if utf8.RuneCountInString(query) > MaxDirectoryQueryLength {
		return nil, fmt.Errorf("%w: at most %d characters", ErrQueryTooLong, MaxDirectoryQueryLength)
	}
	if utf8.RuneCountInString(query) < MinDirectoryQueryLength {
		return []Person{}, nil
	}

	switch {
	case limit <= 0:
		limit = DirectorySearchDefaultLimit
	case limit > DirectorySearchMaxLimit:
		limit = DirectorySearchMaxLimit
	}

	found, err := s.users.Search(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Person, 0, len(found))
	for _, u := range found {
		out = append(out, Person{UserID: u.ID, Login: u.Login, FullName: u.FullName, Email: shownEmail(u.Email, query)})
	}
	return out, nil
}

// shownEmail is the address as Person publishes it: whole when query is a
// case-insensitive substring of it (what the repository's ILIKE matched),
// masked otherwise, and "" for an account without one.
func shownEmail(email, query string) string {
	if email == "" || strings.Contains(strings.ToLower(email), strings.ToLower(query)) {
		return email
	}
	return maskEmail(email)
}

// maskEmail keeps the local part's first character and the domain:
// "sergei@example.edu" becomes "s…@example.edu". An address without "@"
// cannot be split, so it keeps only its first character.
func maskEmail(email string) string {
	local, domain, found := strings.Cut(email, "@")
	first, _ := utf8.DecodeRuneInString(local)
	if first == utf8.RuneError {
		first = 0
	}
	var masked strings.Builder
	if first != 0 {
		masked.WriteRune(first)
	}
	masked.WriteString("…")
	if found {
		masked.WriteString("@" + domain)
	}
	return masked.String()
}
