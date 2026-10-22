package users_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/google/uuid"
)

// mailUnitOfWork stands in for a transaction: what the outbox takes while it
// is open is committed when the function succeeds and dropped when it fails.
type mailUnitOfWork struct {
	calls  int
	open   bool
	outbox *memoryOutbox
}

func (u *mailUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	u.calls++
	u.open = true
	err := fn(ctx)
	u.open = false
	if err != nil {
		u.outbox.pending = nil
		return err
	}
	u.outbox.queued = append(u.outbox.queued, u.outbox.pending...)
	u.outbox.pending = nil
	return nil
}

// memoryOutbox is mailing.Outbox's behaviour without a table. It notes a
// message queued outside the transaction, and wakes that come before the
// message they announce is committed.
type memoryOutbox struct {
	enabled bool
	uow     *mailUnitOfWork
	pending []mailing.Message
	queued  []mailing.Message
	// outside counts messages queued with no transaction open.
	outside int
	wakes   int
	// earlyWakes counts wakes while a message was still uncommitted.
	earlyWakes int
	err        error
}

func (o *memoryOutbox) Enabled() bool { return o.enabled }

func (o *memoryOutbox) Enqueue(_ context.Context, m mailing.Message) error {
	if !o.enabled {
		return mailing.ErrDisabled
	}
	if o.err != nil {
		return o.err
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if !o.uow.open {
		o.outside++
		o.queued = append(o.queued, m)
		return nil
	}
	o.pending = append(o.pending, m)
	return nil
}

func (o *memoryOutbox) Wake() {
	o.wakes++
	if o.uow.open && len(o.pending) > 0 {
		o.earlyWakes++
	}
}

type fixedSettings struct {
	values settings.Values
	reads  int
	err    error
}

func (s *fixedSettings) All(context.Context) (settings.Values, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return s.values, nil
}

type mailFixture struct {
	service  *users.Service
	repo     *userstest.Repository
	sink     *collectingSink
	uow      *mailUnitOfWork
	outbox   *memoryOutbox
	settings *fixedSettings
	actor    uuid.UUID
}

// newMailFixture builds the service with mail on and the installation's
// sign-in letters written in Russian.
func newMailFixture(t *testing.T) *mailFixture {
	t.Helper()
	repo := userstest.New()
	repo.GrantRole("student")
	repo.GrantRole(users.RoleAdmin)
	sink := &collectingSink{}
	outbox := &memoryOutbox{enabled: true}
	uow := &mailUnitOfWork{outbox: outbox}
	outbox.uow = uow
	values := &fixedSettings{values: settings.Values{
		settings.KeyName:         "Olimpiada USM",
		settings.KeyContact:      "olimpiada@usm.md",
		settings.KeyMailLanguage: string(mailing.Russian),
	}}
	service := users.NewService(repo, audit.New(sink), uow, passwordtest.NewHasher()).
		WithMail(users.Mail{
			Outbox:   outbox,
			Texts:    mailing.NewTexts("https://olympdb.usm.md"),
			Settings: values,
		})
	return &mailFixture{
		service: service, repo: repo, sink: sink, uow: uow, outbox: outbox, settings: values,
		actor: uuid.New(),
	}
}

func (f *mailFixture) add(login, email string) users.User {
	return f.repo.Add(users.User{Login: login, FullName: "Ana Rusu " + login, Email: email})
}

var byEmail = users.Delivery{ByEmail: true}

// assertPasswordOnlyInBody checks where a one-time password went: into the
// body of the message for its account, and into no subject and no audit
// entry.
func (f *mailFixture) assertPasswordOnlyInBody(t *testing.T, m mailing.Message, password string) {
	t.Helper()
	if !strings.Contains(m.Body, password) {
		t.Errorf("the message to %s does not carry the password it was issued", m.To)
	}
	if strings.Contains(m.Subject, password) {
		t.Errorf("the subject to %s carries the password", m.To)
	}
	for _, e := range f.sink.entries {
		if strings.Contains(fmt.Sprint(e.Payload), password) {
			t.Errorf("the %s audit entry carries the password", e.Action)
		}
	}
}

func TestCreateQueuesTheSignInDetailsInTheTransactionThatIssuesThePassword(t *testing.T) {
	f := newMailFixture(t)

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "a.rusu", FullName: "Ana Rusu", Email: "ana@usm.md",
		Roles: []string{"student"}, Delivery: byEmail,
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if !created.Email.Emailed || created.Email.SkipReason != "" {
		t.Errorf("Email = %+v, want emailed", created.Email)
	}
	if created.OneTimePassword == "" {
		t.Error("the administrator was not handed the password as well")
	}
	if len(f.outbox.queued) != 1 {
		t.Fatalf("queued %d messages, want 1", len(f.outbox.queued))
	}
	m := f.outbox.queued[0]
	if m.Kind != mailing.KindSignInDetails || m.To != "ana@usm.md" || m.UserID != created.User.ID {
		t.Errorf("message = kind %q to %q for %v, want the sign-in details to ana@usm.md for %v",
			m.Kind, m.To, m.UserID, created.User.ID)
	}
	for _, detail := range []string{"Ana Rusu", "a.rusu", "Olimpiada USM", "olimpiada@usm.md", "https://olympdb.usm.md/login"} {
		if !strings.Contains(m.Body, detail) {
			t.Errorf("the body does not name %q", detail)
		}
	}
	if !strings.Contains(m.Subject, "данные для входа") {
		t.Errorf("subject = %q, want the installation's language (Russian)", m.Subject)
	}
	f.assertPasswordOnlyInBody(t, m, created.OneTimePassword)
	if f.outbox.outside != 0 {
		t.Error("the message was queued outside the transaction that issued the password")
	}
	if f.outbox.wakes != 1 || f.outbox.earlyWakes != 0 {
		t.Errorf("woke the sender %d times (%d before the commit), want once after it",
			f.outbox.wakes, f.outbox.earlyWakes)
	}
	if f.uow.calls != 1 {
		t.Errorf("ran %d units of work, want 1", f.uow.calls)
	}
	want := []string{audit.ActionUserCreate, audit.ActionUserSignInDetailsEmailed}
	if got := f.sink.actions(); !slices.Equal(got, want) {
		t.Errorf("audit actions = %v, want %v", got, want)
	}
	if e := f.sink.entries[1]; e.EntityID != created.User.ID.String() || e.ActorID == nil || *e.ActorID != f.actor {
		t.Errorf("the emailed entry names %s by %v, want the new account by the administrator", e.EntityID, e.ActorID)
	}
}

func TestTheSignInMessageIsWrittenInTheInstallationsLanguage(t *testing.T) {
	f := newMailFixture(t)
	f.settings.values[settings.KeyMailLanguage] = string(mailing.English)

	if _, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "a.rusu", FullName: "Ana Rusu", Email: "ana@usm.md", Delivery: byEmail,
	}); err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if got := f.outbox.queued[0].Subject; got != "Your sign-in details for Olimpiada USM" {
		t.Errorf("subject = %q, want the English one", got)
	}
}

func TestAnAccountWithoutAnAddressIsReportedNotFailed(t *testing.T) {
	f := newMailFixture(t)

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "a.rusu", FullName: "Ana Rusu", Delivery: byEmail,
	})
	if err != nil {
		t.Fatalf("Create() = %v, want the account created without an email", err)
	}

	if created.Email.Emailed || created.Email.SkipReason != users.EmailSkipNoAddress {
		t.Errorf("Email = %+v, want not emailed for %q", created.Email, users.EmailSkipNoAddress)
	}
	if created.OneTimePassword == "" {
		t.Error("the administrator was not handed the password")
	}
	if len(f.outbox.queued) != 0 || f.outbox.wakes != 0 {
		t.Errorf("queued %d and woke %d times for an account with no address", len(f.outbox.queued), f.outbox.wakes)
	}
	if slices.Contains(f.sink.actions(), audit.ActionUserSignInDetailsEmailed) {
		t.Error("the trail says sign-in details were emailed to an account with no address")
	}
}

func TestWithoutTheOptionNothingIsQueuedAndNothingIsRead(t *testing.T) {
	f := newMailFixture(t)

	created, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "a.rusu", FullName: "Ana Rusu", Email: "ana@usm.md",
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	if created.Email != (users.EmailOutcome{}) {
		t.Errorf("Email = %+v, want nothing said when email was not asked for", created.Email)
	}
	if len(f.outbox.queued) != 0 || f.settings.reads != 0 {
		t.Errorf("queued %d messages and read the settings %d times; email was not asked for",
			len(f.outbox.queued), f.settings.reads)
	}
}

// Every route that issues a password, asked to email it while mail is off.
func issuingOperations(f *mailFixture, target users.User) map[string]func() error {
	ctx := context.Background()
	return map[string]func() error{
		"create": func() error {
			_, err := f.service.Create(ctx, users.CreateCommand{
				ActorID: f.actor, Login: "new", FullName: "New One", Email: "new@usm.md", Delivery: byEmail,
			})
			return err
		},
		"import": func() error {
			_, err := f.service.Import(ctx, users.ImportCommand{
				ActorID: f.actor, Delivery: byEmail,
				Rows: []users.ImportRow{{Login: "new", FullName: "New One", Email: "new@usm.md"}},
			})
			return err
		},
		"reset": func() error {
			_, err := f.service.ResetPassword(ctx, f.actor, target.ID, byEmail)
			return err
		},
		"bulk reset": func() error {
			_, err := f.service.BulkResetPassword(ctx, f.actor, []uuid.UUID{target.ID}, byEmail)
			return err
		},
	}
}

func TestMailOffRefusesTheOptionBeforeAnythingChanges(t *testing.T) {
	for name, build := range map[string]func(t *testing.T) *mailFixture{
		"outbox off": func(t *testing.T) *mailFixture {
			f := newMailFixture(t)
			f.outbox.enabled = false
			return f
		},
		"no mail wired": func(t *testing.T) *mailFixture {
			f := newMailFixture(t)
			f.service = users.NewService(f.repo, audit.New(f.sink), f.uow, passwordtest.NewHasher())
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			target := f.add("a.rusu", "ana@usm.md")
			for op, run := range issuingOperations(f, target) {
				t.Run(op, func(t *testing.T) {
					if err := run(); !errors.Is(err, users.ErrMailOff) {
						t.Fatalf("err = %v, want ErrMailOff", err)
					}
					if _, err := f.repo.ByLogin(context.Background(), "new"); !errors.Is(err, users.ErrNotFound) {
						t.Error("an account was created although the request was refused")
					}
					after, _ := f.repo.Get(target.ID)
					if after.PasswordHash != target.PasswordHash || after.SessionGeneration != target.SessionGeneration {
						t.Error("the password was reset although the request was refused")
					}
					if f.uow.calls != 0 || len(f.sink.entries) != 0 || f.settings.reads != 0 {
						t.Errorf("%d units of work, %d audit entries, %d settings reads; want none",
							f.uow.calls, len(f.sink.entries), f.settings.reads)
					}
				})
			}
		})
	}
}

func TestTheMessageIsDroppedWithTheAccountWhenTheTransactionFails(t *testing.T) {
	f := newMailFixture(t)
	f.sink.err = context.DeadlineExceeded

	_, err := f.service.Create(context.Background(), users.CreateCommand{
		ActorID: f.actor, Login: "a.rusu", FullName: "Ana Rusu", Email: "ana@usm.md", Delivery: byEmail,
	})

	if err == nil {
		t.Fatal("Create() succeeded although the audit write failed")
	}
	if len(f.outbox.queued) != 0 {
		t.Error("a message survived the transaction that failed")
	}
	if f.outbox.wakes != 0 {
		t.Error("the sender was woken for a message that was rolled back")
	}
}

// The unit of work returning the outbox's failure is what rolls the reset
// back; the PostgreSQL test proves the rollback itself.
func TestAnOutboxFailureFailsTheReset(t *testing.T) {
	f := newMailFixture(t)
	f.outbox.err = errors.New("the outbox table is unreachable")

	_, err := f.service.ResetPassword(context.Background(), f.actor, f.add("a.rusu", "ana@usm.md").ID, byEmail)

	if !errors.Is(err, f.outbox.err) {
		t.Fatalf("err = %v, want the outbox's failure", err)
	}
	if f.outbox.wakes != 0 {
		t.Error("the sender was woken for a reset that failed")
	}
}

func TestImportEmailsEveryCreatedAccountThatHasAnAddress(t *testing.T) {
	f := newMailFixture(t)
	f.add("taken", "")

	result, err := f.service.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor, Delivery: byEmail, Roles: []string{"student"},
		Rows: []users.ImportRow{
			{Login: "s.popescu", FullName: "Sergiu Popescu", Email: "sergiu@usm.md"},
			{Login: "i.ivanov", FullName: "Ivan Ivanov"},
			{Login: "taken", FullName: "Taken Login", Email: "taken@usm.md"},
			{Login: "a.rusu", FullName: "Ana Rusu", Email: "ana@usm.md"},
		},
	})
	if err != nil {
		t.Fatalf("Import() = %v", err)
	}

	outcome := map[string]users.EmailOutcome{}
	password := map[string]string{}
	for _, c := range result.Created {
		outcome[c.User.Login] = c.Email
		password[c.User.Login] = c.OneTimePassword
	}
	want := map[string]users.EmailOutcome{
		"s.popescu": {Emailed: true},
		"i.ivanov":  {SkipReason: users.EmailSkipNoAddress},
		"a.rusu":    {Emailed: true},
	}
	if fmt.Sprint(outcome) != fmt.Sprint(want) {
		t.Errorf("outcomes = %v, want %v", outcome, want)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Login != "taken" {
		t.Errorf("skipped = %+v, want the taken login alone", result.Skipped)
	}
	if len(f.outbox.queued) != 2 {
		t.Fatalf("queued %d messages, want 2", len(f.outbox.queued))
	}
	for _, m := range f.outbox.queued {
		login := map[string]string{"sergiu@usm.md": "s.popescu", "ana@usm.md": "a.rusu"}[m.To]
		if login == "" {
			t.Errorf("a message went to %q, which no created row gave", m.To)
			continue
		}
		f.assertPasswordOnlyInBody(t, m, password[login])
	}
	if f.outbox.outside != 0 || f.outbox.earlyWakes != 0 {
		t.Error("a message was queued or announced outside its row's transaction")
	}
	if f.settings.reads != 1 {
		t.Errorf("read the settings %d times, want once for the roster", f.settings.reads)
	}
}

func TestResetPasswordEmailsTheNewPassword(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("a.rusu", "ana@usm.md")

	issued, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail)
	if err != nil {
		t.Fatalf("ResetPassword() = %v", err)
	}

	after, _ := f.repo.Get(target.ID)
	if !passwordtest.Matches(t, after.PasswordHash, issued.OneTimePassword) {
		t.Error("the password handed back is not the one stored")
	}
	if !issued.Email.Emailed || issued.ID != target.ID || issued.Login != "a.rusu" {
		t.Errorf("issued = %+v, want the account's emailed reset", issued)
	}
	if len(f.outbox.queued) != 1 || f.outbox.queued[0].UserID != target.ID {
		t.Fatalf("queued %+v, want one message for the account", f.outbox.queued)
	}
	f.assertPasswordOnlyInBody(t, f.outbox.queued[0], issued.OneTimePassword)
	want := []string{audit.ActionUserPasswordReset, audit.ActionUserSignInDetailsEmailed}
	if got := f.sink.actions(); !slices.Equal(got, want) {
		t.Errorf("audit actions = %v, want %v", got, want)
	}
	if f.outbox.wakes != 1 || f.outbox.earlyWakes != 0 {
		t.Errorf("woke %d times (%d early), want once after the commit", f.outbox.wakes, f.outbox.earlyWakes)
	}
}

func TestResetPasswordOfAnAccountWithoutAnAddressStillIssuesIt(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("a.rusu", "")

	issued, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail)
	if err != nil {
		t.Fatalf("ResetPassword() = %v", err)
	}

	if issued.OneTimePassword == "" || issued.Email != (users.EmailOutcome{SkipReason: users.EmailSkipNoAddress}) {
		t.Errorf("issued = %+v, want a password and %q", issued, users.EmailSkipNoAddress)
	}
	if len(f.outbox.queued) != 0 {
		t.Error("a message was queued for an account with no address")
	}
}

func TestBulkResetEmailsEachAccountItsOwnPasswordInOneTransaction(t *testing.T) {
	f := newMailFixture(t)
	first := f.add("s.popescu", "sergiu@usm.md")
	second := f.add("a.rusu", "ana@usm.md")
	noAddress := f.add("i.ivanov", "")
	gone := f.add("gone", "gone@usm.md")
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{gone.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "left", By: f.actor}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	res, err := f.service.BulkResetPassword(context.Background(), f.actor,
		[]uuid.UUID{first.ID, second.ID, noAddress.ID, gone.ID}, byEmail)
	if err != nil {
		t.Fatalf("BulkResetPassword() = %v", err)
	}

	byLogin := map[string]users.IssuedPassword{}
	for _, one := range res.Issued {
		byLogin[one.Login] = one
	}
	if len(res.Issued) != 3 || len(res.Skipped) != 1 || res.Skipped[0].Reason != users.SkipDeleted {
		t.Fatalf("issued %d, skipped %+v; want 3 issued and the deleted account skipped", len(res.Issued), res.Skipped)
	}
	if byLogin["i.ivanov"].Email != (users.EmailOutcome{SkipReason: users.EmailSkipNoAddress}) {
		t.Errorf("the account without an address reads %+v", byLogin["i.ivanov"].Email)
	}
	if len(f.outbox.queued) != 2 {
		t.Fatalf("queued %d messages, want 2", len(f.outbox.queued))
	}
	for _, m := range f.outbox.queued {
		var issued users.IssuedPassword
		switch m.UserID {
		case first.ID:
			issued = byLogin["s.popescu"]
		case second.ID:
			issued = byLogin["a.rusu"]
		default:
			t.Fatalf("a message went to account %v, which was not emailed", m.UserID)
		}
		if !issued.Email.Emailed {
			t.Errorf("%s was queued a message but reads %+v", issued.Login, issued.Email)
		}
		f.assertPasswordOnlyInBody(t, m, issued.OneTimePassword)
	}
	if f.uow.calls != 1 || f.outbox.outside != 0 {
		t.Errorf("%d units of work, %d messages outside them; want everything in one", f.uow.calls, f.outbox.outside)
	}
	if f.outbox.wakes != 1 || f.outbox.earlyWakes != 0 {
		t.Errorf("woke %d times (%d early), want once after the commit", f.outbox.wakes, f.outbox.earlyWakes)
	}
	emailed := 0
	for _, e := range f.sink.entries {
		if e.Action == audit.ActionUserSignInDetailsEmailed {
			emailed++
		}
	}
	if emailed != 2 {
		t.Errorf("recorded %d emailed entries, want 2", emailed)
	}
}

// The installation's name is required by the settings, so a message that
// cannot be written is a fault of the installation, not of a row.
func TestAMessageThatCannotBeWrittenChangesNothing(t *testing.T) {
	f := newMailFixture(t)
	f.settings.values[settings.KeyName] = " "
	target := f.add("a.rusu", "ana@usm.md")

	for op, run := range issuingOperations(f, target) {
		t.Run(op, func(t *testing.T) {
			if err := run(); !errors.Is(err, mailing.ErrIncompleteMessage) {
				t.Fatalf("err = %v, want ErrIncompleteMessage", err)
			}
			if _, err := f.repo.ByLogin(context.Background(), "new"); !errors.Is(err, users.ErrNotFound) {
				t.Error("an account was created without its message")
			}
			after, _ := f.repo.Get(target.ID)
			if after.PasswordHash != target.PasswordHash {
				t.Error("a password was reset without its message")
			}
			if f.uow.calls != 0 || len(f.outbox.queued) != 0 {
				t.Errorf("%d units of work and %d messages, want none", f.uow.calls, len(f.outbox.queued))
			}
		})
	}
}

func TestAnImportWhoseMessageCannotBeWrittenStopsAndNamesTheRows(t *testing.T) {
	f := newMailFixture(t)
	f.settings.values[settings.KeyName] = ""

	result, err := f.service.Import(context.Background(), users.ImportCommand{
		ActorID: f.actor, Delivery: byEmail,
		Rows: []users.ImportRow{
			{Login: "s.popescu", FullName: "Sergiu Popescu", Email: "sergiu@usm.md"},
			{Login: "a.rusu", FullName: "Ana Rusu"},
		},
	})

	if !errors.Is(err, mailing.ErrIncompleteMessage) {
		t.Fatalf("err = %v, want the import stopped by ErrIncompleteMessage", err)
	}
	if len(result.Created) != 0 || len(result.Skipped) != 0 ||
		!slices.Equal(result.NotImported, []string{"s.popescu", "a.rusu"}) {
		t.Errorf("result = %+v, want every row not imported and none skipped", result)
	}
}

func TestASettingsReadFailureChangesNothing(t *testing.T) {
	f := newMailFixture(t)
	f.settings.err = errors.New("settings table unreachable")
	target := f.add("a.rusu", "ana@usm.md")

	for op, run := range issuingOperations(f, target) {
		t.Run(op, func(t *testing.T) {
			if err := run(); !errors.Is(err, f.settings.err) {
				t.Fatalf("err = %v, want the settings failure", err)
			}
			if f.uow.calls != 0 {
				t.Errorf("%d units of work ran, want none", f.uow.calls)
			}
		})
	}
}

func TestWithMailRefusesAMissingPart(t *testing.T) {
	complete := users.Mail{
		Outbox:   &memoryOutbox{},
		Texts:    mailing.NewTexts("https://olympdb.usm.md"),
		Settings: &fixedSettings{},
	}
	for name, mail := range map[string]users.Mail{
		"outbox":   {Texts: complete.Texts, Settings: complete.Settings},
		"texts":    {Outbox: complete.Outbox, Settings: complete.Settings},
		"settings": {Outbox: complete.Outbox, Texts: complete.Texts},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("WithMail accepted a missing part")
				}
			}()
			users.NewService(userstest.New(), audit.New(&collectingSink{}), &userstest.SpyUnitOfWork{},
				passwordtest.NewHasher()).WithMail(mail)
		})
	}
}
