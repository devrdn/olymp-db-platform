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

// The picker shows any non-empty Email, so a missing one must be "".
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

// Neither query may reach the repository.
func TestSearchPeopleReturnsNothingBelowTheMinimumLength(t *testing.T) {
	f := conteststest.NewFixture()
	// Matches "a", so only a gap in the bound could produce a result.
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

// CLAUDE.md rule 2.
func TestSearchPeopleRefusesAnOverlongQuery(t *testing.T) {
	f := conteststest.NewFixture()

	_, err := f.Service.SearchPeople(context.Background(), strings.Repeat("a", contests.MaxDirectoryQueryLength+1), 0)

	if !errors.Is(err, contests.ErrQueryTooLong) {
		t.Fatalf("SearchPeople() = %v, want ErrQueryTooLong", err)
	}
}

// Both bounds count runes, as pg_trgm does; a Cyrillic character is two
// bytes in UTF-8.
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

// A blocked account can never sign in, so appointing or enrolling it would
// be a success that can never take effect.
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

// Limit 0 is what an omitted query parameter decodes to.
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
