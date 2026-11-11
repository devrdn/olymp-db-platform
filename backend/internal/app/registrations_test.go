package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
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
	return registrationAccounts{users: service, hasher: passwordtest.NewHasher()}, repo
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

// Parse's bounds are the account's: whatever it takes, users.Service takes,
// and a byte more is what users.Service would refuse as a 500.
func TestWhatParseTakesAtItsBoundsMakesAnAccount(t *testing.T) {
	accounts, _ := newRegistrationAccounts(t)
	local := strings.Repeat("a", registrations.MaxEmailBytes-len("@example.com"))
	a, err := registrations.Parse(strings.Repeat("ș", 50), strings.Repeat("ș", 49)+"a", local+"@Example.com")
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if len(a.FullName()) != registrations.MaxFullNameBytes || len(a.EmailKey) != registrations.MaxEmailBytes {
		t.Fatalf("full name %d bytes, key %d bytes; want both at the bound", len(a.FullName()), len(a.EmailKey))
	}

	got, err := accounts.Create(t.Context(), registrations.NewAccount{Login: a.EmailKey, Email: a.Email, FullName: a.FullName()})
	if err != nil || got.Existing {
		t.Fatalf("Create() = %+v, %v; want the account made", got, err)
	}

	_, err = accounts.Create(t.Context(), registrations.NewAccount{
		Login: "bob@example.com", Email: "bob@example.com", FullName: a.FullName() + "a",
	})
	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("a full name a byte longer: Create() = %v, want users.ErrInvalidAccount", err)
	}
	_, err = accounts.Create(t.Context(), registrations.NewAccount{
		Login: "a" + a.EmailKey, Email: "a" + a.Email, FullName: "Bob Ionescu",
	})
	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("a login a byte longer: Create() = %v, want users.ErrInvalidAccount", err)
	}
}

func TestARegistrationsPasswordWaitsOnlyTheSignInWait(t *testing.T) {
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	slot, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() = %v", err)
	}
	t.Cleanup(slot.Release)
	repo := userstest.New()
	repo.GrantRole("student")
	accounts := registrationAccounts{users: users.NewService(repo, audit.New(discardAudit{}), &userstest.SpyUnitOfWork{}, hasher)}
	started := time.Now()

	_, err = accounts.Create(t.Context(), registrations.NewAccount{Login: "ana@example.com", FullName: "Ana Popescu"})

	if !errors.Is(err, password.ErrBusy) || time.Since(started) > time.Second {
		t.Errorf("Create() = %v after %v, want password.ErrBusy after the sign-in wait", err, time.Since(started))
	}
}

func TestADecoyHashWaitsOnlyTheSignInWait(t *testing.T) {
	accounts, _ := newRegistrationAccounts(t)
	if err := accounts.Decoy(t.Context()); err != nil {
		t.Fatalf("Decoy() = %v", err)
	}

	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	slot, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() = %v", err)
	}
	t.Cleanup(slot.Release)
	accounts.hasher = hasher

	if err := accounts.Decoy(t.Context()); !errors.Is(err, password.ErrBusy) {
		t.Errorf("Decoy() on a full hasher = %v, want password.ErrBusy like Create", err)
	}
}
