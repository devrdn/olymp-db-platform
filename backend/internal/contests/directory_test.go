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
	}
}

// TestSearchPeopleReturnsNothingForAnEmptyQuery guards against the picker
// becoming a way to browse the whole installation's roster: search-as-you-type
// only ever asks once somebody has typed something, so nothing typed answers
// nothing found rather than the first page of every account.
func TestSearchPeopleReturnsNothingForAnEmptyQuery(t *testing.T) {
	f := conteststest.NewFixture()
	f.Users.Add(users.User{Login: "s.ivanov", FullName: "Ivanov", Status: users.StatusActive})

	found, err := f.Service.SearchPeople(context.Background(), "   ", 0)
	if err != nil {
		t.Fatalf("SearchPeople(empty) = %v", err)
	}
	if len(found) != 0 {
		t.Errorf("SearchPeople(empty) = %+v, want no results", found)
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
