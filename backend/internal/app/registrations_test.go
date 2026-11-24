package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/audit"
	"github.com/devrdn/olymp-db-platform/backend/internal/mailing"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/password"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/olymp-db-platform/backend/internal/registrations"
	"github.com/devrdn/olymp-db-platform/backend/internal/registrations/registrationstest"
	"github.com/devrdn/olymp-db-platform/backend/internal/settings"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
	"github.com/google/uuid"
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
		Email: "Ana@example.com", FullName: "Ana Popescu",
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
		Email: "ana@example.com", FullName: "Ana Popescu",
	})
	if err != nil || !got.Existing || got.OneTimePassword != "" {
		t.Errorf("a taken login: Create() = %+v, %v; want the existing account and no password", got, err)
	}

	// The email's uniqueness is the database's index, met at the insert.
	repo.Err = users.ErrEmailTaken
	got, err = accounts.Create(t.Context(), registrations.NewAccount{
		Email: "bob@example.com", FullName: "Bob Ionescu",
	})
	if err != nil || !got.Existing || got.OneTimePassword != "" {
		t.Errorf("a taken email: Create() = %+v, %v; want the existing account and no password", got, err)
	}
}

// Parse's bounds are the account's: whatever it takes at a bound,
// users.Service takes, and users.Service refuses a byte more.
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

	got, err := accounts.Create(t.Context(), registrations.NewAccount{Email: a.Email, FullName: a.FullName()})
	if err != nil || got.Existing {
		t.Fatalf("Create() = %+v, %v; want the account made", got, err)
	}

	_, err = accounts.Create(t.Context(), registrations.NewAccount{
		Email: "bob@example.com", FullName: a.FullName() + "a",
	})
	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("a full name a byte longer: Create() = %v, want users.ErrInvalidAccount", err)
	}
	_, err = accounts.Create(t.Context(), registrations.NewAccount{
		Email: "a" + a.Email, FullName: "Bob Ionescu",
	})
	if !errors.Is(err, users.ErrInvalidAccount) {
		t.Errorf("a login a byte longer: Create() = %v, want users.ErrInvalidAccount", err)
	}
}

// The registration form answers its own refusals; an address or a name Parse
// accepts and the account then refuses would reach the person as a refusal
// they cannot read. Every candidate Parse takes must make an account.
func TestWhatParseAcceptsTheAccountAccepts(t *testing.T) {
	type row struct{ first, last, email string }
	accepted := []row{
		{"Ana", "Popescu", "Ana.Popescu@Example.com"},
		{"\u0218tefan", "\u021aurcan", "\u0218tefan@usm.md"},
		{"\u0410\u043b\u0451\u043d\u0430", "O'Neil", "alena@example.com"},
		{"Jos\u0065\u0301", "D\u2019Artagnan", "jose.d@example.com"},
		{"J. R. R.", "Tolkien", "j.r.r@example.com"},
		{"\u0639\u0644\u06cc\u200c\u0631\u0636\u0627", "\u0645\u062d\u0645\u062f\u06cc", "ali@example.com"},
		{"\u0d30\u0d3e\u0d1c\u0d28\u0d4d\u200d", "\u182e\u1823\u180b\u1829\u182d\u1823\u182f", "rajan@example.com"},
	}
	candidates := append([]row{
		{"Ana", "Popescu", "a\u200bna@example.com"},
		{"Ana", "Popescu", "ana\u034f@example.com"},
		{"Ana", "Popescu", "\u0130na@example.com"},
		{"Ana", "Popescu", "\u0130na\u023A@example.com"},
		{"Ana", "Popescu", "ana@exa\u202emple.com"},
		{"\u3164", "Popescu", "ana@example.com"},
		{"\u0301", "Popescu", "ana@example.com"},
		{"Ana\ufe0f", "Popescu", "ana@example.com"},
		{"Ana", "Popescu", "ana\u180b@example.com"},
		{"Ana\u17b4", "Popescu", "ana@example.com"},
		{"Ion\u200cPopescu", "Popescu", "ion@example.com"},
	}, accepted...)

	taken := 0
	for i, c := range candidates {
		a, err := registrations.Parse(c.first, c.last, c.email)
		if err != nil {
			continue
		}
		taken++
		accounts, _ := newRegistrationAccounts(t)
		if _, err := accounts.Create(t.Context(), registrations.NewAccount{Email: a.Email, FullName: a.FullName()}); err != nil {
			t.Errorf("row %d (%q, %q, %q): Parse took it, Create() = %v", i, c.first, c.last, c.email, err)
		}
	}
	if taken != len(accepted) {
		t.Errorf("Parse took %d rows, want exactly the %d meant to be taken", taken, len(accepted))
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

	_, err = accounts.Create(t.Context(), registrations.NewAccount{Email: "ana@example.com", FullName: "Ana Popescu"})

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

// mailOutbox is registrationstest's outbox with the abandon call users.Service
// also needs.
type mailOutbox struct{ *registrationstest.Outbox }

func (mailOutbox) AbandonPending(context.Context, mailing.Kind, []uuid.UUID, string) error {
	return nil
}

// A taken login is refused before any hashing, so with mail on the adapter
// spends the hash itself; without mail it does not.
func TestATakenLoginSpendsAHashOnlyWithMailOn(t *testing.T) {
	repo := userstest.New()
	repo.GrantRole("student")
	repo.Add(users.User{Login: "ana@example.com", FullName: "Ana", Status: users.StatusActive})
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	service := users.NewService(repo, audit.New(discardAudit{}), &userstest.SpyUnitOfWork{}, hasher).WithMail(users.Mail{
		Outbox: mailOutbox{&registrationstest.Outbox{}}, Texts: mailing.NewTexts("https://olympdb.usm.md"),
		Settings: registrationstest.Settings{Values: settings.Values{
			settings.KeyName: "Olimpiada USM", settings.KeyMailLanguage: string(mailing.English),
		}},
	})
	accounts := registrationAccounts{users: service, hasher: hasher}
	n := registrations.NewAccount{Email: "ana@example.com", FullName: "Ana Popescu"}
	slot, err := hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() = %v", err)
	}
	t.Cleanup(slot.Release)

	if got, err := accounts.Create(t.Context(), n); err != nil || !got.Existing {
		t.Errorf("without mail: Create() = %+v, %v, want an existing account at no cost", got, err)
	}
	n.ByEmail = true
	if _, err := accounts.Create(t.Context(), n); !errors.Is(err, password.ErrBusy) {
		t.Errorf("with mail: Create() = %v, want password.ErrBusy from the decoy hash", err)
	}
}
