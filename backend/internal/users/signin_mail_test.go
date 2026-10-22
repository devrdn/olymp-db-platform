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

// mailUnitOfWork stands in for a transaction: what the outbox takes or gives
// up while it is open lands when the function succeeds and is dropped when it
// fails.
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
	u.outbox.settle(err == nil)
	return err
}

// outboxRow is one message in the memory outbox.
type outboxRow struct {
	mailing.Message
	committed bool
	abandoned string
	// abandoning is a reason waiting for the transaction to commit.
	abandoning string
}

// memoryOutbox behaves as mailing.Outbox does over its table: a batch
// enqueue wakes nobody, and abandoning works with mail off too. It notes
// writes made outside the transaction, and wakes made inside it.
type memoryOutbox struct {
	enabled bool
	uow     *mailUnitOfWork
	rows    []*outboxRow
	// outside counts writes made with no transaction open.
	outside int
	wakes   int
	// earlyWakes counts wakes while the transaction was still open.
	earlyWakes int
	err        error
}

func (o *memoryOutbox) Enabled() bool { return o.enabled }

func (o *memoryOutbox) EnqueueMany(_ context.Context, ms []mailing.Message) error {
	if !o.enabled {
		return mailing.ErrDisabled
	}
	if o.err != nil {
		return o.err
	}
	for _, m := range ms {
		if err := m.Validate(); err != nil {
			return err
		}
	}
	if !o.uow.open {
		o.outside++
	}
	for _, m := range ms {
		o.rows = append(o.rows, &outboxRow{Message: m, committed: !o.uow.open})
	}
	return nil
}

func (o *memoryOutbox) AbandonPending(_ context.Context, kind mailing.Kind, users []uuid.UUID, reason string) error {
	if o.err != nil {
		return o.err
	}
	if !o.uow.open {
		o.outside++
	}
	for _, row := range o.rows {
		if row.Kind != kind || row.abandoned != "" || !slices.Contains(users, row.UserID) {
			continue
		}
		if o.uow.open {
			row.abandoning = reason
		} else {
			row.abandoned = reason
		}
	}
	return nil
}

func (o *memoryOutbox) Wake() {
	o.wakes++
	if o.uow.open {
		o.earlyWakes++
	}
}

func (o *memoryOutbox) settle(commit bool) {
	kept := o.rows[:0]
	for _, row := range o.rows {
		switch {
		case commit:
			row.committed = true
			if row.abandoning != "" {
				row.abandoned = row.abandoning
			}
		case !row.committed:
			continue
		}
		row.abandoning = ""
		kept = append(kept, row)
	}
	o.rows = kept
}

// queued is every committed message still waiting to be sent.
func (o *memoryOutbox) queued() []mailing.Message {
	var out []mailing.Message
	for _, row := range o.rows {
		if row.committed && row.abandoned == "" {
			out = append(out, row.Message)
		}
	}
	return out
}

// abandonedFor lists why each of an account's messages was given up.
func (o *memoryOutbox) abandonedFor(user uuid.UUID) []string {
	var out []string
	for _, row := range o.rows {
		if row.UserID == user && row.abandoned != "" {
			out = append(out, row.abandoned)
		}
	}
	return out
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
	f := &mailFixture{repo: repo, sink: sink, uow: uow, outbox: outbox, settings: values, actor: uuid.New()}
	f.serve(repo)
	return f
}

// serve builds the service over repo, which may wrap the fixture's own.
func (f *mailFixture) serve(repo users.Repository) {
	f.service = users.NewService(repo, audit.New(f.sink), f.uow, passwordtest.NewHasher()).
		WithMail(users.Mail{
			Outbox:   f.outbox,
			Texts:    mailing.NewTexts("https://olympdb.usm.md"),
			Settings: f.settings,
		})
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
	if len(f.outbox.queued()) != 1 {
		t.Fatalf("queued %d messages, want 1", len(f.outbox.queued()))
	}
	m := f.outbox.queued()[0]
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

	if got := f.outbox.queued()[0].Subject; got != "Your sign-in details for Olimpiada USM" {
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
	if len(f.outbox.queued()) != 0 || f.outbox.wakes != 0 {
		t.Errorf("queued %d and woke %d times for an account with no address", len(f.outbox.queued()), f.outbox.wakes)
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
	if len(f.outbox.queued()) != 0 || f.settings.reads != 0 {
		t.Errorf("queued %d messages and read the settings %d times; email was not asked for",
			len(f.outbox.queued()), f.settings.reads)
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
	if len(f.outbox.queued()) != 0 {
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
	if len(f.outbox.queued()) != 2 {
		t.Fatalf("queued %d messages, want 2", len(f.outbox.queued()))
	}
	for _, m := range f.outbox.queued() {
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
	if len(f.outbox.queued()) != 1 || f.outbox.queued()[0].UserID != target.ID {
		t.Fatalf("queued %+v, want one message for the account", f.outbox.queued())
	}
	f.assertPasswordOnlyInBody(t, f.outbox.queued()[0], issued.OneTimePassword)
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
	if len(f.outbox.queued()) != 0 {
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
	if len(f.outbox.queued()) != 2 {
		t.Fatalf("queued %d messages, want 2", len(f.outbox.queued()))
	}
	for _, m := range f.outbox.queued() {
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
			if f.uow.calls != 0 || len(f.outbox.queued()) != 0 {
				t.Errorf("%d units of work and %d messages, want none", f.uow.calls, len(f.outbox.queued()))
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

func TestBulkResetEmailsNoAccountThatIsNotActive(t *testing.T) {
	f := newMailFixture(t)
	first := f.add("s.popescu", "sergiu@usm.md")
	blocked := f.add("blocked", "blocked@usm.md")
	second := f.add("a.rusu", "ana@usm.md")
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{blocked.ID}, users.StatusBlocked,
		users.StatusChange{Reason: "cheating", By: f.actor}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	res, err := f.service.BulkResetPassword(context.Background(), f.actor,
		[]uuid.UUID{first.ID, blocked.ID, second.ID}, byEmail)
	if err != nil {
		t.Fatalf("BulkResetPassword() = %v", err)
	}

	if len(res.Issued) != 3 {
		t.Fatalf("issued %d passwords, want 3: a blocked account's password is still reset", len(res.Issued))
	}
	for _, one := range res.Issued {
		want := users.EmailOutcome{Emailed: true}
		if one.ID == blocked.ID {
			want = users.EmailOutcome{SkipReason: users.EmailSkipNotActive}
		}
		if one.Email != want {
			t.Errorf("%s reads %+v, want %+v", one.Login, one.Email, want)
		}
		if one.OneTimePassword == "" {
			t.Errorf("%s was handed no password", one.Login)
		}
	}
	after, _ := f.repo.Get(blocked.ID)
	if !passwordtest.Matches(t, after.PasswordHash, res.Issued[1].OneTimePassword) {
		t.Error("the blocked account's password was not reset")
	}
	for _, m := range f.outbox.queued() {
		if m.UserID == blocked.ID {
			t.Error("a message was queued to the blocked account")
		}
	}
	if len(f.outbox.queued()) != 2 {
		t.Errorf("queued %d messages, want 2", len(f.outbox.queued()))
	}
}

func TestResetPasswordOfABlockedAccountStillIssuesItWithoutEmail(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("blocked", "blocked@usm.md")
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{target.ID}, users.StatusBlocked,
		users.StatusChange{Reason: "cheating", By: f.actor}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}

	issued, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail)
	if err != nil {
		t.Fatalf("ResetPassword() = %v", err)
	}

	if issued.OneTimePassword == "" || issued.Email != (users.EmailOutcome{SkipReason: users.EmailSkipNotActive}) {
		t.Errorf("issued = %+v, want a password and %q", issued, users.EmailSkipNotActive)
	}
	if len(f.outbox.queued()) != 0 || slices.Contains(f.sink.actions(), audit.ActionUserSignInDetailsEmailed) {
		t.Error("a blocked account was emailed its sign-in details")
	}
}

func (f *mailFixture) block(t *testing.T, id uuid.UUID) {
	t.Helper()
	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{id}, users.StatusBlocked,
		users.StatusChange{Reason: "cheating", By: f.actor}); err != nil {
		t.Fatalf("SetStatus() = %v", err)
	}
}

func TestResettingTwiceLeavesOnlyTheNewestMessagePending(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("a.rusu", "ana@usm.md")
	ctx := context.Background()

	if _, err := f.service.ResetPassword(ctx, f.actor, target.ID, byEmail); err != nil {
		t.Fatalf("first ResetPassword() = %v", err)
	}
	second, err := f.service.ResetPassword(ctx, f.actor, target.ID, byEmail)
	if err != nil {
		t.Fatalf("second ResetPassword() = %v", err)
	}

	queued := f.outbox.queued()
	if len(queued) != 1 || !strings.Contains(queued[0].Body, second.OneTimePassword) {
		t.Fatalf("pending %d messages, want only the one with the newest password", len(queued))
	}
	if got := f.outbox.abandonedFor(target.ID); len(got) != 1 || !strings.Contains(got[0], "superseded") {
		t.Errorf("given up %v, want the first message superseded", got)
	}
	if f.outbox.outside != 0 {
		t.Error("a message was given up outside the transaction that reset the password")
	}
}

func TestAnyNewPasswordGivesUpTheEarlierMessage(t *testing.T) {
	for name, issue := range map[string]func(f *mailFixture, id uuid.UUID) error{
		"reset without email": func(f *mailFixture, id uuid.UUID) error {
			_, err := f.service.ResetPassword(context.Background(), f.actor, id, users.Delivery{})
			return err
		},
		"reset with mail off since": func(f *mailFixture, id uuid.UUID) error {
			f.outbox.enabled = false
			_, err := f.service.ResetPassword(context.Background(), f.actor, id, users.Delivery{})
			return err
		},
		"bulk reset": func(f *mailFixture, id uuid.UUID) error {
			_, err := f.service.BulkResetPassword(context.Background(), f.actor, []uuid.UUID{id}, users.Delivery{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMailFixture(t)
			target := f.add("a.rusu", "ana@usm.md")
			if _, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail); err != nil {
				t.Fatalf("ResetPassword() = %v", err)
			}

			if err := issue(f, target.ID); err != nil {
				t.Fatalf("issuing again = %v", err)
			}

			if len(f.outbox.queued()) != 0 {
				t.Error("the message with the dead password is still waiting to be sent")
			}
		})
	}
}

func TestABulkResetGivesUpOnlyTheEarlierMessagesOfItsAccounts(t *testing.T) {
	f := newMailFixture(t)
	first := f.add("s.popescu", "sergiu@usm.md")
	second := f.add("a.rusu", "ana@usm.md")
	untouched := f.add("i.ivanov", "ivan@usm.md")
	ctx := context.Background()
	if _, err := f.service.BulkResetPassword(ctx, f.actor, []uuid.UUID{first.ID, second.ID, untouched.ID}, byEmail); err != nil {
		t.Fatalf("first BulkResetPassword() = %v", err)
	}

	again, err := f.service.BulkResetPassword(ctx, f.actor, []uuid.UUID{first.ID, second.ID}, byEmail)
	if err != nil {
		t.Fatalf("second BulkResetPassword() = %v", err)
	}

	newest := map[uuid.UUID]string{}
	for _, one := range again.Issued {
		newest[one.ID] = one.OneTimePassword
	}
	queued := f.outbox.queued()
	if len(queued) != 3 {
		t.Fatalf("pending %d messages, want 3: the two new ones and the untouched account's", len(queued))
	}
	for _, m := range queued {
		if password, ok := newest[m.UserID]; ok && !strings.Contains(m.Body, password) {
			t.Errorf("the message pending for %v does not carry its newest password", m.UserID)
		}
	}
	if len(f.outbox.abandonedFor(untouched.ID)) != 0 {
		t.Error("an account outside the selection lost its message")
	}
}

func TestAFailedResetKeepsTheEarlierMessage(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("a.rusu", "ana@usm.md")
	if _, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail); err != nil {
		t.Fatalf("ResetPassword() = %v", err)
	}
	f.sink.err = context.DeadlineExceeded

	if _, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail); err == nil {
		t.Fatal("the second reset succeeded although its audit write failed")
	}

	if len(f.outbox.queued()) != 1 || len(f.outbox.abandonedFor(target.ID)) != 0 {
		t.Error("a reset that rolled back still gave up the message whose password stands")
	}
}

func TestBlockingOrDeletingGivesUpThePendingMessage(t *testing.T) {
	for name, close := range map[string]func(f *mailFixture, id uuid.UUID) error{
		"block": func(f *mailFixture, id uuid.UUID) error {
			return f.service.Block(context.Background(), f.actor, id, "cheating")
		},
		"delete": func(f *mailFixture, id uuid.UUID) error {
			return f.service.Delete(context.Background(), f.actor, id, "left the university")
		},
		"bulk block": func(f *mailFixture, id uuid.UUID) error {
			_, err := f.service.BulkSetStatus(context.Background(), f.actor, []uuid.UUID{id}, users.StatusBlocked, "cheating")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMailFixture(t)
			target := f.add("a.rusu", "ana@usm.md")
			if _, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail); err != nil {
				t.Fatalf("ResetPassword() = %v", err)
			}

			if err := close(f, target.ID); err != nil {
				t.Fatalf("closing the account = %v", err)
			}

			if len(f.outbox.queued()) != 0 {
				t.Error("the closed account's sign-in details are still waiting to be sent")
			}
			if got := f.outbox.abandonedFor(target.ID); len(got) != 1 || !strings.Contains(got[0], "blocked or deleted") {
				t.Errorf("given up %v, want the message withdrawn for the closed account", got)
			}
			if f.outbox.outside != 0 {
				t.Error("the message was given up outside the status change's transaction")
			}
		})
	}
}

func TestUnblockingGivesUpNothing(t *testing.T) {
	f := newMailFixture(t)
	target := f.add("a.rusu", "ana@usm.md")
	f.block(t, target.ID)
	other := f.add("i.ivanov", "ivan@usm.md")
	if _, err := f.service.ResetPassword(context.Background(), f.actor, other.ID, byEmail); err != nil {
		t.Fatalf("ResetPassword() = %v", err)
	}

	if err := f.service.Unblock(context.Background(), f.actor, target.ID); err != nil {
		t.Fatalf("Unblock() = %v", err)
	}

	if len(f.outbox.queued()) != 1 {
		t.Error("unblocking an account gave up a message")
	}
}

// blockedDuringWrite blocks one account as the password write runs, as an
// administrator's block committing between the first read and the write.
type blockedDuringWrite struct {
	*userstest.Repository
	t      *testing.T
	f      *mailFixture
	target uuid.UUID
}

func (r blockedDuringWrite) SetPassword(ctx context.Context, id uuid.UUID, hash string, mustChange bool) error {
	r.f.block(r.t, r.target)
	return r.Repository.SetPassword(ctx, id, hash, mustChange)
}

func (r blockedDuringWrite) SetPasswordMany(ctx context.Context, creds []users.Credential) error {
	r.f.block(r.t, r.target)
	return r.Repository.SetPasswordMany(ctx, creds)
}

func TestAnAccountBlockedDuringAResetIsNotEmailed(t *testing.T) {
	t.Run("bulk", func(t *testing.T) {
		f := newMailFixture(t)
		active := f.add("s.popescu", "sergiu@usm.md")
		target := f.add("a.rusu", "ana@usm.md")
		f.serve(blockedDuringWrite{Repository: f.repo, t: t, f: f, target: target.ID})

		res, err := f.service.BulkResetPassword(context.Background(), f.actor, []uuid.UUID{active.ID, target.ID}, byEmail)
		if err != nil {
			t.Fatalf("BulkResetPassword() = %v", err)
		}

		for _, one := range res.Issued {
			want := users.EmailOutcome{Emailed: true}
			if one.ID == target.ID {
				want = users.EmailOutcome{SkipReason: users.EmailSkipNotActive}
			}
			if one.Email != want || one.OneTimePassword == "" {
				t.Errorf("%s reads %+v (password %v), want %+v with a password", one.Login, one.Email,
					one.OneTimePassword != "", want)
			}
		}
		queued := f.outbox.queued()
		if len(queued) != 1 || queued[0].UserID != active.ID {
			t.Errorf("pending %+v, want only the active account's message", queued)
		}
		if n := len(f.sink.entries); f.sink.entries[n-1].EntityID != active.ID.String() {
			t.Error("the trail says the blocked account was emailed")
		}
	})
	t.Run("single", func(t *testing.T) {
		f := newMailFixture(t)
		target := f.add("a.rusu", "ana@usm.md")
		f.serve(blockedDuringWrite{Repository: f.repo, t: t, f: f, target: target.ID})

		issued, err := f.service.ResetPassword(context.Background(), f.actor, target.ID, byEmail)
		if err != nil {
			t.Fatalf("ResetPassword() = %v", err)
		}

		if issued.Email != (users.EmailOutcome{SkipReason: users.EmailSkipNotActive}) || issued.OneTimePassword == "" {
			t.Errorf("issued %+v, want a password and not_active", issued)
		}
		if len(f.outbox.queued()) != 0 || f.outbox.wakes != 0 {
			t.Errorf("pending %d messages and %d wakes, want none", len(f.outbox.queued()), f.outbox.wakes)
		}
		if slices.Contains(f.sink.actions(), audit.ActionUserSignInDetailsEmailed) {
			t.Error("the trail says the blocked account was emailed")
		}
	})
}
