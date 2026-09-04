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
