package app

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
)

type discardAudit struct{}

func (discardAudit) Append(context.Context, audit.Entry) error       { return nil }
func (discardAudit) AppendMany(context.Context, []audit.Entry) error { return nil }

func newRegistrationAccounts(t *testing.T) (registrationAccounts, *userstest.Repository) {
	t.Helper()
	repo := userstest.New()
	repo.GrantRole("student")
	service := users.NewService(repo, audit.New(discardAudit{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher())
	return registrationAccounts{users: service}, repo
}

func TestARegistrationMakesAStudentAccount(t *testing.T) {
	accounts, repo := newRegistrationAccounts(t)

	got, err := accounts.Create(t.Context(), registrations.NewAccount{
		Login: "ana@example.com", Email: "Ana@example.com", FullName: "Ana Popescu",
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if got.Existing || got.Login != "ana@example.com" || got.OneTimePassword == "" {
		t.Errorf("Create() = %+v, want a new account with its password", got)
	}
	stored, err := repo.ByID(t.Context(), got.ID)
	if err != nil {
		t.Fatalf("ByID() = %v", err)
	}
	if len(stored.Roles) != 1 || stored.Roles[0] != "student" || !stored.MustChangePassword {
		t.Errorf("stored = %+v, want a student who must replace the password", stored)
	}
}

func TestATakenLoginOrEmailIsAnExistingAccount(t *testing.T) {
	accounts, repo := newRegistrationAccounts(t)
	repo.Add(users.User{Login: "ana@example.com", FullName: "Ana", Status: users.StatusActive})

	got, err := accounts.Create(t.Context(), registrations.NewAccount{
		Login: "ana@example.com", Email: "ana@example.com", FullName: "Ana Popescu",
	})
	if err != nil || !got.Existing || got.OneTimePassword != "" {
		t.Errorf("a taken login: Create() = %+v, %v; want the existing account and no password", got, err)
	}

	// The email's uniqueness is the database's index, met at the insert.
	repo.Err = users.ErrEmailTaken
	got, err = accounts.Create(t.Context(), registrations.NewAccount{
		Login: "bob@example.com", Email: "bob@example.com", FullName: "Bob Ionescu",
	})
	if err != nil || !got.Existing || got.OneTimePassword != "" {
		t.Errorf("a taken email: Create() = %+v, %v; want the existing account and no password", got, err)
	}
}
