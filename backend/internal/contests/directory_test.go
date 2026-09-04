package contests_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// TestSearchPeopleMatchesLoginNameOrEmail pins the owner's own requirement:
// the picker matches on login or email, and also on a name, because that is
// what an administrator actually remembers (see users.Filter.Query, which
// this reuses through contests.UserDirectory.Search).
func TestSearchPeopleMatchesLoginNameOrEmail(t *testing.T) {
	f := conteststest.NewFixture()
	target := f.Users.Add(users.User{
		Login: "s.ivanov", FullName: "Ivanov Sergei", Email: "sergei@example.edu",
		Status: users.StatusActive,
	})
	f.Users.Add(users.User{
		Login: "other", FullName: "Someone Else", Email: "else@example.edu",
		Status: users.StatusActive,
	})

	for _, query := range []string{"ivanov", "Sergei", "sergei@example"} {
		found, err := f.Service.SearchPeople(context.Background(), query, 0)
		if err != nil {
			t.Fatalf("SearchPeople(%q) = %v", query, err)
		}
		if len(found) != 1 || found[0].UserID != target.ID {
			t.Errorf("SearchPeople(%q) = %+v, want only %v", query, found, target.ID)
		}
		if found[0].Email != target.Email {
			t.Errorf("SearchPeople(%q) email = %q, want %q", query, found[0].Email, target.Email)
		}
	}
}

// TestSearchPeopleReturnsACleanEmptyEmailForAnAccountWithoutOne guards the
// other half of the owner's decision to publish the email: an account that
// never set one must come back with the field simply empty, not with some
// value that reads as an address nobody actually gave — the picker later
// treats a non-empty Email as one worth showing.
func TestSearchPeopleReturnsACleanEmptyEmailForAnAccountWithoutOne(t *testing.T) {
	f := conteststest.NewFixture()
	target := f.Users.Add(users.User{Login: "s.noemail-ivanov", FullName: "Ivanov", Status: users.StatusActive})

	found, err := f.Service.SearchPeople(context.Background(), "ivanov", 0)
	if err != nil {
		t.Fatalf("SearchPeople() = %v", err)
	}
	if len(found) != 1 || found[0].UserID != target.ID {
		t.Fatalf("SearchPeople() = %+v, want only %v", found, target.ID)
	}
	if found[0].Email != "" {
		t.Errorf("SearchPeople() email = %q, want an empty string for an account with none", found[0].Email)
	}
}

// TestSearchPeopleReturnsNothingBelowTheMinimumLength guards the common,
// cheap case a directory search should not do: answer a single keystroke —
// `?q=a`, the incident MinDirectoryQueryLength was added for — with a page
// of unrelated accounts. It is not a defence against a determined
// enumeration; see that constant's own comment for what actually bounds who
// may run this search. An empty box and a lone character both come back
// with nothing, and neither ever reaches the repository.
func TestSearchPeopleReturnsNothingBelowTheMinimumLength(t *testing.T) {
	f := conteststest.NewFixture()
	// A login containing "a" so a gap in the bound — not a lack of matching
	// data — is the only way this test could see a result.
	f.Users.Add(users.User{Login: "a-ivanov", FullName: "Ivanov", Status: users.StatusActive})

	for _, query := range []string{"", "   ", "a"} {
		found, err := f.Service.SearchPeople(context.Background(), query, 0)
		if err != nil {
			t.Fatalf("SearchPeople(%q) = %v", query, err)
		}
		if len(found) != 0 {
			t.Errorf("SearchPeople(%q) = %+v, want nothing below the minimum length", query, found)
		}
	}
}

// TestSearchPeopleSearchesAtTheMinimumLength proves the bound is inclusive:
// exactly MinDirectoryQueryLength characters is enough to run a search, not
// one more.
func TestSearchPeopleSearchesAtTheMinimumLength(t *testing.T) {
	f := conteststest.NewFixture()
	needle := strings.Repeat("z", contests.MinDirectoryQueryLength)
	target := f.Users.Add(users.User{
		Login: needle + "-ivanov", FullName: "Ivanov", Status: users.StatusActive,
	})
	f.Users.Add(users.User{Login: "unrelated", FullName: "Someone Else", Status: users.StatusActive})

	found, err := f.Service.SearchPeople(context.Background(), needle, 0)
	if err != nil {
		t.Fatalf("SearchPeople(%q) = %v", needle, err)
	}
	if len(found) != 1 || found[0].UserID != target.ID {
		t.Errorf("SearchPeople(%q) = %+v, want only %v", needle, found, target.ID)
	}
}

// TestSearchPeopleRefusesAnOverlongQuery is the domain's own bound
// (CLAUDE.md rule 2): a search box is typed by hand a few characters at a
// time, and nothing legitimate needs more than MaxDirectoryQueryLength.
func TestSearchPeopleRefusesAnOverlongQuery(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.SearchPeople(context.Background(), strings.Repeat("a", contests.MaxDirectoryQueryLength+1), 0)

	if !errors.Is(err, contests.ErrQueryTooLong) {
		t.Fatalf("SearchPeople() = %v, want ErrQueryTooLong", err)
	}
}

// TestSearchPeopleCountsTheQueryBoundInCharactersNotBytes guards the two
// bounds on the same field agreeing on what they count. MinDirectoryQueryLength
// was already a rune count (it has to be, to match what pg_trgm extracts a
// trigram from); MaxDirectoryQueryLength used to be checked with len(), which
// counts bytes. A hundred-character Cyrillic query is about two hundred bytes
// in UTF-8, so it used to be refused by a message claiming "at most 100
// characters" — a limit it had not actually reached.
func TestSearchPeopleCountsTheQueryBoundInCharactersNotBytes(t *testing.T) {
	f := conteststest.NewFixture()

	atTheLimit := strings.Repeat("ф", contests.MaxDirectoryQueryLength)
	if _, err := f.Service.SearchPeople(context.Background(), atTheLimit, 0); err != nil {
		t.Errorf("SearchPeople() at the character limit = %v, want no error", err)
	}

	overTheLimit := strings.Repeat("ф", contests.MaxDirectoryQueryLength+1)
	if _, err := f.Service.SearchPeople(context.Background(), overTheLimit, 0); !errors.Is(err, contests.ErrQueryTooLong) {
		t.Errorf("SearchPeople() over the character limit = %v, want ErrQueryTooLong", err)
	}
}

// TestSearchPeopleExcludesABlockedAccount guards the other half of Finding 1:
// a blocked account can never sign in (auth.Service.Login and
// auth.Middleware both refuse it), so offering it as a candidate here would
// let staff appoint or enrol somebody who can never act on it, and be told
// the server succeeded.
func TestSearchPeopleExcludesABlockedAccount(t *testing.T) {
	f := conteststest.NewFixture()
	f.Users.Add(users.User{Login: "s.blocked-ivanov", FullName: "Ivanov", Status: users.StatusBlocked})
	live := f.Users.Add(users.User{Login: "s.live-ivanov", FullName: "Ivanov", Status: users.StatusActive})

	found, err := f.Service.SearchPeople(context.Background(), "ivanov", 0)
	if err != nil {
		t.Fatalf("SearchPeople() = %v", err)
	}
	if len(found) != 1 || found[0].UserID != live.ID {
		t.Errorf("SearchPeople() = %+v, want only the live account %v", found, live.ID)
	}
}

// TestSearchPeopleClampsAnUnreasonableLimit proves the picker's own ceiling
// holds regardless of what a caller asks for — a typeahead needs a handful of
// matches to disambiguate, not a page of the whole install.
func TestSearchPeopleClampsAnUnreasonableLimit(t *testing.T) {
	f := conteststest.NewFixture()
	for i := 0; i < contests.DirectorySearchMaxLimit+5; i++ {
		f.Users.Add(users.User{
			Login: fmt.Sprintf("student-%02d", i), FullName: "Student", Status: users.StatusActive,
		})
	}

	found, err := f.Service.SearchPeople(context.Background(), "student", 1000)
	if err != nil {
		t.Fatalf("SearchPeople() = %v", err)
	}
	if len(found) != contests.DirectorySearchMaxLimit {
		t.Errorf("SearchPeople() returned %d, want the ceiling %d", len(found), contests.DirectorySearchMaxLimit)
	}
}

// TestSearchPeopleDefaultsAnUnsetLimit proves a caller asking for nothing in
// particular (limit 0, what an omitted query parameter decodes to) still gets
// a usable page rather than none at all.
func TestSearchPeopleDefaultsAnUnsetLimit(t *testing.T) {
	f := conteststest.NewFixture()
	f.Users.Add(users.User{Login: "s.ivanov", FullName: "Ivanov", Status: users.StatusActive})

	found, err := f.Service.SearchPeople(context.Background(), "ivanov", 0)
	if err != nil {
		t.Fatalf("SearchPeople() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("SearchPeople() = %+v, want the one match", found)
	}
}
