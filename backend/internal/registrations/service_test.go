package registrations_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/devrdn/db-contest/backend/internal/registrations/registrationstest"
	"github.com/google/uuid"
)

func applicant(t *testing.T, email string) registrations.Applicant {
	t.Helper()
	a, err := registrations.Parse("Ana", "Popescu", email)
	if err != nil {
		t.Fatalf("Parse(%q) = %v", email, err)
	}
	return a
}

func TestAFirstRegistrationCreatesTheAccountAndMailsTheCode(t *testing.T) {
	f := registrationstest.NewFixture(nil)

	out, err := f.Service.Register(t.Context(), applicant(t, "Ana@Example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	if out.Status != registrations.StatusCreated {
		t.Errorf("status = %q, want created", out.Status)
	}
	code := out.Registration.Code
	if len(code) != registrations.CodeLength || strings.Trim(code, registrations.CodeAlphabet) != "" {
		t.Errorf("code %q is not %d characters of the alphabet", code, registrations.CodeLength)
	}
	if out.Account.Existing || out.Account.Login != "ana@example.com" || out.Account.OneTimePassword == "" {
		t.Errorf("account = %+v, want a new one under the lowercased email", out.Account)
	}
	if out.Registration.UserID == nil || *out.Registration.UserID != out.Account.ID {
		t.Errorf("registration links %v, want the new account %v", out.Registration.UserID, out.Account.ID)
	}
	want := registrations.NewAccount{Login: "ana@example.com", Email: "Ana@Example.com", FullName: "Ana Popescu", ByEmail: true}
	if len(f.Accounts.Created) != 1 || f.Accounts.Created[0] != want {
		t.Errorf("accounts created = %+v, want %+v", f.Accounts.Created, want)
	}

	queued := f.Outbox.Queued()
	if len(queued) != 1 {
		t.Fatalf("queued %d messages, want the code", len(queued))
	}
	m := queued[0]
	if m.Kind != mailing.KindEventRegistration || m.UserID != uuid.Nil || m.To != "Ana@Example.com" {
		t.Errorf("message = %+v, want the code to the typed address, to no account", m)
	}
	if !strings.Contains(m.Body, registrations.DisplayCode(code)) || !strings.Contains(m.Body, "ana@example.com") {
		t.Errorf("body does not carry the code and the login:\n%s", m.Body)
	}
	if !out.EmailSent || f.Outbox.Wakes != 1 {
		t.Errorf("email sent = %v, wakes = %d; want the message queued and the sender woken", out.EmailSent, f.Outbox.Wakes)
	}
}

func TestWithMailOffTheAccountIsMadeWithoutEmail(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Outbox.Off = true

	out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	if out.EmailSent || len(f.Outbox.Queued()) != 0 {
		t.Errorf("email sent = %v with mail off", out.EmailSent)
	}
	if len(f.Accounts.Created) != 1 || f.Accounts.Created[0].ByEmail {
		t.Errorf("accounts created = %+v, want one without email delivery", f.Accounts.Created)
	}
	if out.Account.OneTimePassword == "" {
		t.Error("the screen is the only place the password appears, and it has none")
	}
}

// A taken login or email is not a failure: the person signs in with the
// account they have.
func TestAnExistingAccountStillGetsTheRegistration(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Accounts.Taken = map[string]bool{"ana@example.com": true}

	out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	if out.Status != registrations.StatusCreated || !out.Account.Existing || out.Account.OneTimePassword != "" {
		t.Errorf("outcome = %+v, want a registration with the existing account and no password", out)
	}
	if out.Registration.UserID != nil {
		t.Errorf("registration links %v, want no account", out.Registration.UserID)
	}
	queued := f.Outbox.Queued()
	if len(queued) != 1 || !strings.Contains(queued[0].Body, "lost your password") {
		t.Errorf("queued %+v, want the code with the lost-password line", queued)
	}
}

func TestARepeatKeepsTheCodeAndMailsItAgainAfterTheCooldown(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	first, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	f.Store.Advance(registrations.ResendCooldown)
	again, err := f.Service.Register(t.Context(), applicant(t, "  ANA@example.com"))
	if err != nil {
		t.Fatalf("repeat Register() = %v", err)
	}

	if again.Status != registrations.StatusExisting || !again.EmailSent {
		t.Errorf("repeat = %+v, want existing with the code mailed again", again)
	}
	if again.Registration.Code != first.Registration.Code || len(f.Store.Rows()) != 1 {
		t.Errorf("repeat code %q, %d rows; want the same code in one row", again.Registration.Code, len(f.Store.Rows()))
	}
	if again.Account != (registrations.Account{}) || len(f.Accounts.Created) != 1 {
		t.Errorf("repeat account = %+v, %d created; want the account untouched", again.Account, len(f.Accounts.Created))
	}
	queued := f.Outbox.Queued()
	if len(queued) != 2 {
		t.Fatalf("queued %d messages, want the code twice", len(queued))
	}
	resent := queued[1].Body
	if !strings.Contains(resent, registrations.DisplayCode(first.Registration.Code)) ||
		!strings.Contains(resent, "ana@example.com") || !strings.Contains(resent, "lost your password") {
		t.Errorf("resent body should carry the code, the login and the lost-password line:\n%s", resent)
	}
	if f.Outbox.Wakes != 2 {
		t.Errorf("wakes = %d, want one per queued message", f.Outbox.Wakes)
	}
}

func TestARepeatWithinTheCooldownQueuesNothing(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	if _, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com")); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	f.Store.Advance(registrations.ResendCooldown - 1)
	again, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("repeat Register() = %v", err)
	}

	if again.Status != registrations.StatusExisting || again.EmailSent {
		t.Errorf("repeat = %+v, want existing with nothing sent", again)
	}
	if n := len(f.Outbox.Queued()); n != 1 {
		t.Errorf("queued %d messages, want only the first", n)
	}
}

func TestARepeatWithMailOffSendsNothing(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Outbox.Off = true
	if _, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com")); err != nil {
		t.Fatalf("Register() = %v", err)
	}

	again, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("repeat Register() = %v", err)
	}
	if again.Status != registrations.StatusExisting || again.EmailSent {
		t.Errorf("repeat = %+v, want existing with nothing sent", again)
	}
}

func TestAFullEventRefusesNewEmailsButNotRepeats(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	if _, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com")); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	f.Store.Fill(registrations.Capacity - 1)

	if _, err := f.Service.Register(t.Context(), applicant(t, "bob@example.com")); !errors.Is(err, registrations.ErrFull) {
		t.Errorf("a new email: Register() = %v, want ErrFull", err)
	}
	if len(f.Accounts.Created) != 1 {
		t.Errorf("%d accounts created, want none for the refused email", len(f.Accounts.Created))
	}
	if out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com")); err != nil || out.Status != registrations.StatusExisting {
		t.Errorf("a repeat: Register() = %+v, %v, want existing", out, err)
	}
}

// repeating yields the same bytes forever, so every code it draws is the same.
type repeating byte

func (r repeating) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestACodeCollisionIsRetriedThenGivenUp(t *testing.T) {
	var source bytes.Buffer
	f := registrationstest.NewFixture(func(c *registrations.Config) { c.Random = &source })
	// Six identical draws, then six of another value.
	source.Write(bytes.Repeat([]byte{0}, 2*registrations.CodeLength))
	source.Write(bytes.Repeat([]byte{1}, registrations.CodeLength))

	first, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	second, err := f.Service.Register(t.Context(), applicant(t, "bob@example.com"))
	if err != nil {
		t.Fatalf("second Register() = %v, want the collision retried", err)
	}
	if first.Registration.Code == second.Registration.Code {
		t.Errorf("both registrations hold %q", first.Registration.Code)
	}

	stuck := registrationstest.NewFixture(func(c *registrations.Config) { c.Random = repeating(0) })
	if _, err := stuck.Service.Register(t.Context(), applicant(t, "ana@example.com")); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	_, err = stuck.Service.Register(t.Context(), applicant(t, "bob@example.com"))
	if err == nil || errors.Is(err, registrations.ErrFull) {
		t.Errorf("a code that always collides: Register() = %v, want an internal error", err)
	}
}

func TestCheckInMarksAndClearsTheArrival(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	staff := uuid.New()

	in, err := f.Service.SetCheckedIn(t.Context(), staff, out.Registration.ID, true)
	if err != nil || in.CheckedInAt == nil {
		t.Fatalf("SetCheckedIn(true) = %+v, %v", in, err)
	}
	cleared, err := f.Service.SetCheckedIn(t.Context(), staff, out.Registration.ID, false)
	if err != nil || cleared.CheckedInAt != nil {
		t.Errorf("SetCheckedIn(false) = %+v, %v", cleared, err)
	}
	if _, err := f.Service.SetCheckedIn(t.Context(), staff, uuid.New(), true); !errors.Is(err, registrations.ErrNotFound) {
		t.Errorf("unknown id: SetCheckedIn() = %v, want ErrNotFound", err)
	}

	entries := f.Audit.Entries
	if len(entries) != 2 {
		t.Fatalf("audit entries = %+v, want one for the check-in and one for its reversal", entries)
	}
	for i, wantCheckedIn := range []bool{true, false} {
		e := entries[i]
		if e.Action != audit.ActionEventRegistrationCheckIn || e.ActorID == nil || *e.ActorID != staff ||
			e.Entity != "event_registration" || e.EntityID != out.Registration.ID.String() ||
			e.Payload["checked_in"] != wantCheckedIn {
			t.Errorf("entry %d = %+v, want the staff member setting checked_in=%v on the registration", i, e, wantCheckedIn)
		}
	}
	if len(f.Audit.Loose) != 0 {
		t.Errorf("entries written outside the update's unit of work: %+v", f.Audit.Loose)
	}
}

// Two submits of one new email at once: both make the account's attempt, the
// one whose account was taken wins the insert, and the one that made the
// account must still hand its password over.
func TestADoubleSubmitKeepsTheNewAccountsPassword(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Store.BeforeInsert = func(r registrations.Registration) {
		f.Store.BeforeInsert = nil
		r.Code = "WWWWWW"
		r.UserID = nil
		f.Store.Put(r)
	}

	out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	if out.Status != registrations.StatusCreated || out.Account.OneTimePassword == "" || out.Account.Existing {
		t.Errorf("outcome = %+v, want created with this request's account and password", out)
	}
	if out.Registration.Code != "WWWWWW" {
		t.Errorf("code = %q, want the winner's", out.Registration.Code)
	}
	rows := f.Store.Rows()
	if len(rows) != 1 || rows[0].UserID == nil || *rows[0].UserID != out.Account.ID {
		t.Errorf("rows = %+v, want the one registration linked to the new account", rows)
	}
}

func TestADoubleSubmitWhoseRegistrationHasAnAccountAnswersAsARepeat(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	other := uuid.New()
	f.Store.BeforeInsert = func(r registrations.Registration) {
		f.Store.BeforeInsert = nil
		r.Code = "WWWWWW"
		r.UserID = &other
		f.Store.Put(r)
	}

	out, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if out.Status != registrations.StatusExisting {
		t.Errorf("outcome = %+v, want existing", out)
	}
}

func TestASettingsFailureMakesNoAccount(t *testing.T) {
	f := registrationstest.NewFixture(func(c *registrations.Config) {
		c.Settings = registrationstest.Settings{Err: errors.New("settings are down")}
	})

	if _, err := f.Service.Register(t.Context(), applicant(t, "ana@example.com")); err == nil {
		t.Fatal("Register() = nil, want the settings failure")
	}
	if len(f.Accounts.Created) != 0 || len(f.Store.Rows()) != 0 {
		t.Errorf("%d accounts, %d rows; want neither", len(f.Accounts.Created), len(f.Store.Rows()))
	}
}

// With mail on, a repeat and an address that already has an account must cost
// what a new account costs, or the response time says which one it was.
func TestWithMailOnTheCasesWithoutANewAccountSpendADecoyHash(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Accounts.Taken = map[string]bool{"known@example.com": true}

	for _, email := range []string{"fresh@example.com", "fresh@example.com", "known@example.com"} {
		if _, err := f.Service.Register(t.Context(), applicant(t, email)); err != nil {
			t.Fatalf("Register(%q) = %v", email, err)
		}
	}

	if f.Accounts.Decoys != 2 {
		t.Errorf("decoy hashes = %d, want 2: the repeat and the known account, not the new account", f.Accounts.Decoys)
	}
}

func TestWithMailOffNoDecoyHashIsSpent(t *testing.T) {
	f := registrationstest.NewFixture(nil)
	f.Outbox.Off = true
	f.Accounts.Taken = map[string]bool{"known@example.com": true}

	for _, email := range []string{"fresh@example.com", "fresh@example.com", "known@example.com"} {
		if _, err := f.Service.Register(t.Context(), applicant(t, email)); err != nil {
			t.Fatalf("Register(%q) = %v", email, err)
		}
	}

	if f.Accounts.Decoys != 0 {
		t.Errorf("decoy hashes = %d, want none: with mail off the answer already tells the cases apart", f.Accounts.Decoys)
	}
}

func TestTheOutcomeSaysWhetherMailIsOn(t *testing.T) {
	for _, off := range []bool{false, true} {
		f := registrationstest.NewFixture(nil)
		f.Outbox.Off = off
		first, _ := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
		repeat, _ := f.Service.Register(t.Context(), applicant(t, "ana@example.com"))
		if first.MailOn == off || repeat.MailOn == off {
			t.Errorf("off=%v: MailOn = %v / %v, want %v for both", off, first.MailOn, repeat.MailOn, !off)
		}
	}
}
