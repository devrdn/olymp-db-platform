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
// Email is an accepted exposure: the search runs behind participant.manage,
// not users.manage, so any holder can read every account's email in the
// installation. It is shown because a login and a full name do not always tell
// two people apart. An account without an email has Email == "".
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
		out = append(out, Person{UserID: u.ID, Login: u.Login, FullName: u.FullName, Email: u.Email})
	}
	return out, nil
}
