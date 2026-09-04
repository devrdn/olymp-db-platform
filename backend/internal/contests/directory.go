package contests

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaxDirectoryQueryLength bounds the text a person may type into the staff
// and participant pickers before it reaches a query. A search box is filled
// in by hand, a few characters at a time; nothing legitimate ever needs more
// than this, and CLAUDE.md's rule 2 is explicit that every field that reaches
// storage — including one that only ever reaches a WHERE clause — carries an
// explicit bound rather than whatever a caller chose to send.
const MaxDirectoryQueryLength = 100

// MinDirectoryQueryLength is the shortest text SearchPeople will turn into a
// database query. Below it — including the empty string — the search behaves
// exactly like nothing having been typed: no results, and no request reaches
// the repository at all. It answered a concrete incident: `?q=a` used to
// return a page of unrelated accounts to the very first keystroke a picker
// sent.
//
// The number is 3, not a rounder-feeling 2, because 3 is what
// migration 000016's trigram indexes actually need: pg_trgm pads a value with
// two leading spaces and one trailing one before cutting it into
// three-character trigrams, so a two-character pattern produces none, and
// PostgreSQL cannot use a trigram index to serve a predicate it extracted no
// trigram from — it falls back to a sequential scan of the whole users table,
// on an endpoint every contest's staff reaches, on every debounced keystroke.
// TestSearchPredicateUsesTheTrigramIndexes (internal/postgres/users_test.go)
// proves this at exactly this length; lowering the constant for a friendlier
// feel silently brings the sequential scan back.
//
// This is also a courtesy for the ordinary case, not a defence against
// enumeration, and it must not be described as one — see SearchPeople's own
// comment for what actually bounds who can run this search at all. Three
// characters still leaves thousands of combinations for a determined
// permission holder to walk through; it only stops the search from
// answering a single keystroke with a page of strangers.
const MinDirectoryQueryLength = 3

// DirectorySearchDefaultLimit and DirectorySearchMaxLimit bound how many
// candidates a picker gets back for one search. A typeahead needs enough
// matches to tell two "Ivanov"s apart, not a page of the whole installation —
// and unlike the administrator's own account listing (Filter, up to 200),
// this search is reachable by every contest's staff, so the ceiling sits
// deliberately lower.
const (
	DirectorySearchDefaultLimit = 10
	DirectorySearchMaxLimit     = 20
)

// ErrQueryTooLong refuses a search string longer than a picker's own input
// could legitimately produce.
var ErrQueryTooLong = errors.New("search text is too long")

// Person is the least an organiser needs to tell two accounts apart when
// searching for somebody to appoint or enrol: a name to read, a login to
// act on.
//
// Deliberately not the wider users.User. This search runs behind
// participant.manage rather than users.manage (internal/rbac), so it reaches
// far more roles than the administrator screens that type was built for, and
// an email address is not what disambiguates two candidates here — the login
// already does, since it is unique. Publishing it here would be the first
// time this search reaches beyond users.manage, and CLAUDE.md's own rule is
// not to share personal data beyond what the task needs.
type Person struct {
	UserID   uuid.UUID
	Login    string
	FullName string
}

// SearchPeople resolves the accounts an organiser might mean when appointing
// staff or adding a participant — one picker behind both actions, since
// either starts with finding a person by a few typed characters.
//
// It is not scoped to a contest: every account in the installation is a
// candidate for staffing or joining any of them. What limits who may call it
// is the contest-scoped participant.manage permission the HTTP layer checks
// before this runs — every contest role that can view a contest's staff and
// participants also carries participant.manage (see rbac's managerPermissions),
// so this reaches exactly the people who already work the people screen, not
// a wider audience than that.
//
// A query shorter than MinDirectoryQueryLength — including an empty one —
// returns no results without ever reaching the database: search-as-you-type
// only asks once somebody has typed something resembling a login or a name.
// That is a guard against the cheap, accidental case, not against
// enumeration: the actual boundary on who may run this search at all is the
// contest-scoped participant.manage permission the HTTP layer checks before
// SearchPeople runs. Somebody who already holds that permission can still
// walk every short combination and, a search at a time, see every account in
// the installation — but a login and a full name are exactly what that
// permission already lets its holder see on any contest's own staff and
// participant lists, so that is not a new capability this endpoint hands
// out, only a faster way to use one it already grants.
func (s *Service) SearchPeople(ctx context.Context, query string, limit int) ([]Person, error) {
	query = strings.TrimSpace(query)
	if len(query) > MaxDirectoryQueryLength {
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
		out = append(out, Person{UserID: u.ID, Login: u.Login, FullName: u.FullName})
	}
	return out, nil
}
