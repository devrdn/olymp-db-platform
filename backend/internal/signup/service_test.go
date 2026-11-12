package signup_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/signup"
	"github.com/devrdn/db-contest/backend/internal/signup/signuptest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

const chosenPassword = "a long enough chosen password"

func start(email, login string) signup.StartCommand {
	return signup.StartCommand{
		FullName: "Ana Popescu", Login: login, Email: email, Password: chosenPassword, Lang: "en",
	}
}

var codePattern = regexp.MustCompile(`\b\d{6}\b`)

// codeIn reads the code out of the message that carries it.
func codeIn(t *testing.T, m mailing.Message) string {
	t.Helper()
	if m.Kind != mailing.KindSignupCode {
		t.Fatalf("message is %s, want %s", m.Kind, mailing.KindSignupCode)
	}
	code := codePattern.FindString(m.Body)
	if code == "" {
		t.Fatalf("no six-digit code in the body:\n%s", m.Body)
	}
	return code
}

// begun is what one Start handed out: the token it answered, and the code
// it mailed, empty when the message went to an account's owner instead.
type begun struct {
	email, code, token string
}

func (b begun) confirm(code string) signup.ConfirmCommand {
	return signup.ConfirmCommand{Email: b.email, Code: code, Token: b.token}
}

func (b begun) resend() signup.ResendCommand {
	return signup.ResendCommand{Email: b.email, Token: b.token, Lang: "en"}
}

// begin runs Start and returns what it handed out.
func begin(t *testing.T, f *signuptest.Fixture, cmd signup.StartCommand) begun {
	t.Helper()
	res, err := f.Service.Start(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	queued := f.Outbox.Queued()
	if len(queued) == 0 {
		t.Fatal("Start queued nothing")
	}
	b := begun{email: cmd.Email, token: res.Token}
	if last := queued[len(queued)-1]; last.Kind == mailing.KindSignupCode {
		b.code = codeIn(t, last)
	}
	return b
}

// startErr runs Start for its error alone.
func startErr(t *testing.T, f *signuptest.Fixture, cmd signup.StartCommand) error {
	t.Helper()
	_, err := f.Service.Start(t.Context(), cmd)
	return err
}

// otherCode is a well-formed code that is not code.
func otherCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func TestStartQueuesACodeAndStoresNoAccount(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	b := begin(t, f, start("ana@example.edu", "ana-popescu"))

	queued := f.Outbox.Queued()
	if len(queued) != 1 {
		t.Fatalf("queued %d messages, want the code", len(queued))
	}
	if m := queued[0]; m.To != "ana@example.edu" || m.UserID != uuid.Nil || !strings.Contains(m.Body, "ana-popescu") {
		t.Errorf("message to %q, account %v; want the typed address, no account and the login it creates:\n%s", m.To, m.UserID, m.Body)
	}
	if f.Outbox.Wakes == 0 {
		t.Error("the sender was not woken")
	}

	rows := f.Store.Rows()
	if len(rows) != 1 {
		t.Fatalf("%d pending sign-ups, want one", len(rows))
	}
	p := rows[0]
	if p.Email != "ana@example.edu" || p.Login != "ana-popescu" || p.FullName != "Ana Popescu" || p.Tries != 0 {
		t.Errorf("pending = %+v, want the form as typed and no tries", p)
	}
	if !passwordtest.Matches(t, p.PasswordHash, chosenPassword) {
		t.Error("the stored digest does not match the chosen password")
	}
	if strings.Contains(string(p.CodeHash), b.code) || strings.Contains(string(p.TokenHash), b.token) {
		t.Error("the code or the token is stored in clear")
	}
	if !p.ExpiresAt.Equal(f.Clock.Now().Add(signup.CodeLifetime)) || !p.SentAt.Equal(f.Clock.Now()) {
		t.Errorf("sent %v, expires %v; want now and %s later", p.SentAt, p.ExpiresAt, signup.CodeLifetime)
	}

	if _, err := f.Users.ByLogin(t.Context(), "ana-popescu"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ByLogin() = %v, want no account before the code is confirmed", err)
	}
}

func TestStartForAKnownEmailAnswersAsForANewOne(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	owner := f.Users.Add(users.User{Login: "ana.owner", Email: "Ana@Example.edu", FullName: "Ana Owner", PasswordHash: "x"})

	if _, err := f.Service.Start(t.Context(), start("ana@example.edu", "someone-else")); err != nil {
		t.Fatalf("Start() for a known email = %v, want the answer a new one gets", err)
	}

	if hashes := f.Hasher.Calls(); hashes != 1 {
		t.Errorf("spent %d password hashes, want the one a new sign-up spends", hashes)
	}
	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Email != "ana@example.edu" || rows[0].Login != "someone-else" {
		t.Errorf("pending = %+v, want the row a new email gets", rows)
	}
	queued := f.Outbox.Queued()
	if len(queued) != 1 {
		t.Fatalf("queued %d messages, want only the existing-account one", len(queued))
	}
	m := queued[0]
	if m.Kind != mailing.KindSignupExisting || m.To != owner.Email || m.UserID != owner.ID {
		t.Errorf("message = %s to %q for %v, want %s to the owner", m.Kind, m.To, m.UserID, mailing.KindSignupExisting)
	}
	if codePattern.MatchString(m.Body) {
		t.Errorf("the owner's message carries a code:\n%s", m.Body)
	}
	if !strings.Contains(m.Body, "ana.owner") || !strings.Contains(m.Body, "Ana Owner") || strings.Contains(m.Body, "someone-else") {
		t.Errorf("body should name the owner's account and nothing the form typed:\n%s", m.Body)
	}
	if f.Outbox.Wakes == 0 {
		t.Error("the sender was not woken")
	}
}

// Recovery never serves staff or a blocked account, so the existing-account
// message must not send them to it.
func TestTheExistingAccountMessageSendsAnAccountRecoveryCannotServeToAnAdministrator(t *testing.T) {
	for name, tc := range map[string]struct {
		owner       users.User
		toForgotten bool
	}{
		"a student": {users.User{Roles: []string{users.RoleStudent}}, true},
		"staff":     {users.User{Roles: []string{"organizer"}}, false},
		"blocked":   {users.User{Roles: []string{users.RoleStudent}, Status: users.StatusBlocked}, false},
	} {
		t.Run(name, func(t *testing.T) {
			f := signuptest.NewFixture(t, nil)
			f.Users.GrantRole("organizer", "contests.manage")
			owner := tc.owner
			owner.Login, owner.Email, owner.FullName, owner.PasswordHash = "ana.owner", "ana@example.edu", "Ana Owner", "x"
			f.Users.Add(owner)

			if _, err := f.Service.Start(t.Context(), start("ana@example.edu", "someone-else")); err != nil {
				t.Fatalf("Start() = %v", err)
			}

			queued := f.Outbox.Queued()
			if len(queued) != 1 || queued[0].Kind != mailing.KindSignupExisting {
				t.Fatalf("queued %+v, want the existing-account message", queued)
			}
			if got := strings.Contains(queued[0].Body, "/forgot"); got != tc.toForgotten {
				t.Errorf("links the forgot page: %v, want %v:\n%s", got, tc.toForgotten, queued[0].Body)
			}
		})
	}
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestStartReturnsATokenForAKnownEmailToo(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	started := knownAndNew(t, f)

	if started[0].token == started[1].token {
		t.Error("two sign-ups were handed the same token")
	}
	for _, b := range started {
		if !tokenPattern.MatchString(b.token) {
			t.Errorf("%s: token %q, want 32 bytes in unpadded base64url", b.email, b.token)
		}
	}
	for _, p := range f.Store.Rows() {
		if len(p.TokenHash) != 32 {
			t.Errorf("%s: stored %d token bytes, want its SHA-256", p.Email, len(p.TokenHash))
		}
	}
}

// knownAndNew starts one sign-up for an address an account holds and one
// for a fresh address.
func knownAndNew(t *testing.T, f *signuptest.Fixture) []begun {
	t.Helper()
	f.Users.Add(users.User{Login: "owner", Email: "known@example.edu", FullName: "Owner", PasswordHash: "x"})
	return []begun{begin(t, f, start("known@example.edu", "known")), begin(t, f, start("new@example.edu", "new"))}
}

func TestConfirmAnswersTheSameForAKnownEmail(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	started := knownAndNew(t, f)

	for try := 1; try <= signup.MaxTries+1; try++ {
		want := signup.ErrWrongCode
		if try > signup.MaxTries {
			want = signup.ErrCodeExpired
		}
		for _, b := range started {
			// No code is 1 000 000; malformed codes count like wrong ones.
			if _, err := f.Service.Confirm(t.Context(), b.confirm("1000000")); !errors.Is(err, want) {
				t.Errorf("try %d for %s = %v, want %v", try, b.email, err, want)
			}
		}
	}

	f2 := signuptest.NewFixture(t, nil)
	started = knownAndNew(t, f2)
	f2.Clock.Advance(signup.CodeLifetime)
	for _, b := range started {
		if _, err := f2.Service.Confirm(t.Context(), b.confirm("123456")); !errors.Is(err, signup.ErrCodeExpired) {
			t.Errorf("thirty minutes on, %s = %v, want ErrCodeExpired", b.email, err)
		}
	}
}

func TestResendAnswersTheSameForAKnownEmail(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	started := knownAndNew(t, f)

	for _, b := range started {
		if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrTooSoon) {
			t.Errorf("Resend(%s) at once = %v, want ErrTooSoon", b.email, err)
		}
	}
	f.Clock.Advance(signup.ResendCooldown)
	for _, b := range started {
		if err := f.Service.Resend(t.Context(), b.resend()); err != nil {
			t.Errorf("Resend(%s) after the cooldown = %v, want nil", b.email, err)
		}
	}
	for _, b := range started {
		if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrTooSoon) {
			t.Errorf("Resend(%s) again at once = %v, want ErrTooSoon", b.email, err)
		}
	}

	kinds := map[string][]mailing.Kind{}
	for _, m := range f.Outbox.Queued() {
		kinds[m.To] = append(kinds[m.To], m.Kind)
	}
	if got := kinds["known@example.edu"]; len(got) != 2 || got[0] != mailing.KindSignupExisting || got[1] != mailing.KindSignupExisting {
		t.Errorf("the owner was sent %v, want the existing-account message twice and never a code", got)
	}
	if got := kinds["new@example.edu"]; len(got) != 2 || got[0] != mailing.KindSignupCode || got[1] != mailing.KindSignupCode {
		t.Errorf("the new address was sent %v, want a code twice", got)
	}
	f.Clock.Advance(signup.CodeLifetime)
	for _, b := range started {
		if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrCodeExpired) {
			t.Errorf("Resend(%s) once expired = %v, want ErrCodeExpired", b.email, err)
		}
	}
}

// Someone who types another person's address while that person is signing
// up replaces the row, but the code then mailed confirms only in the browser
// that started the replacement, which never sees it.
func TestARestartedSignupCannotBeConfirmedFromTheFirstBrowser(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	first := begin(t, f, start("ana@example.edu", "ana"))
	second := begin(t, f, start("ana@example.edu", "intruder"))

	if first.token == second.token {
		t.Fatal("the restart kept the first token")
	}
	for _, code := range []string{first.code, second.code} {
		if _, err := f.Service.Confirm(t.Context(), first.confirm(code)); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("Confirm() from the first browser = %v, want ErrWrongCode", err)
		}
	}
	f.Clock.Advance(signup.ResendCooldown)
	if err := f.Service.Resend(t.Context(), first.resend()); !errors.Is(err, signup.ErrWrongCode) {
		t.Errorf("Resend() from the first browser = %v, want ErrWrongCode", err)
	}
	if _, err := f.Users.ByLogin(t.Context(), "intruder"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ByLogin(intruder) = %v, want no account", err)
	}
	if _, err := f.Service.Confirm(t.Context(), second.confirm(second.code)); err != nil {
		t.Errorf("Confirm() from the browser that restarted = %v, want the account", err)
	}
}

func TestConfirmWithoutTheStartTokenIsAWrongCode(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	other := begin(t, f, start("maria@example.edu", "maria"))

	for i, token := range []string{"", other.token, b.token[:42], b.token + "A", strings.Repeat("A", 10_000)} {
		cmd := b.confirm(b.code)
		cmd.Token = token
		if _, err := f.Service.Confirm(t.Context(), cmd); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("Confirm() with token %.12q = %v, want ErrWrongCode", token, err)
		}
		if tries := f.Store.Rows()[0].Tries; tries != i+1 {
			t.Errorf("after %d tries the row counts %d", i+1, tries)
		}
	}
	if _, err := f.Service.Confirm(t.Context(), b.confirm(b.code)); !errors.Is(err, signup.ErrCodeExpired) {
		t.Errorf("Confirm() after five tokenless tries = %v, want ErrCodeExpired", err)
	}
}

func TestResendNeedsTheStartToken(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	f.Clock.Advance(signup.ResendCooldown)

	for _, token := range []string{"", strings.Repeat("A", 43)} {
		cmd := b.resend()
		cmd.Token = token
		if err := f.Service.Resend(t.Context(), cmd); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("Resend() with token %q = %v, want ErrWrongCode", token, err)
		}
	}
	if n := len(f.Outbox.Queued()); n != 1 {
		t.Errorf("queued %d messages, want only the first code", n)
	}
	if tries := f.Store.Rows()[0].Tries; tries != 2 {
		t.Errorf("tries = %d, want both tokenless resends counted", tries)
	}
	if err := f.Service.Resend(t.Context(), b.resend()); err != nil {
		t.Errorf("Resend() with the token = %v, want nil", err)
	}
}

// A resend renews the code and its window, never the tries: one sign-up gets
// MaxTries guesses in all, however often it asks for a code.
func TestResendNeverResetsTheTries(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	for range signup.MaxTries - 1 {
		if _, err := f.Service.Confirm(t.Context(), b.confirm(otherCode(b.code))); !errors.Is(err, signup.ErrWrongCode) {
			t.Fatalf("Confirm() = %v, want ErrWrongCode", err)
		}
	}
	f.Clock.Advance(signup.ResendCooldown)
	if err := f.Service.Resend(t.Context(), b.resend()); err != nil {
		t.Fatalf("Resend() = %v", err)
	}
	queued := f.Outbox.Queued()
	code := codeIn(t, queued[len(queued)-1])

	if _, err := f.Service.Confirm(t.Context(), b.confirm(otherCode(code))); !errors.Is(err, signup.ErrWrongCode) {
		t.Errorf("the last try = %v, want ErrWrongCode", err)
	}
	if _, err := f.Service.Confirm(t.Context(), b.confirm(code)); !errors.Is(err, signup.ErrCodeExpired) {
		t.Errorf("a sixth try with the resent code = %v, want ErrCodeExpired", err)
	}
}

func TestTheCodeIsTimedFromAfterTheHash(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	f.Hasher.During = func() { f.Clock.Advance(2 * time.Second) }

	begin(t, f, start("ana@example.edu", "ana"))

	if p := f.Store.Rows()[0]; !p.SentAt.Equal(f.Clock.Now()) || !p.ExpiresAt.Equal(f.Clock.Now().Add(signup.CodeLifetime)) {
		t.Errorf("sent %v, expires %v; want the time after the hash, %v", p.SentAt, p.ExpiresAt, f.Clock.Now())
	}
}

func TestStartRefusesAFullNameThatIsNotWrittenAsAName(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	for _, name := range []string{"Visit evil.example now", "https://evil.example", "Ana2", "win $100"} {
		cmd := start("ana@example.edu", "ana")
		cmd.FullName = name
		if err := startErr(t, f, cmd); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("Start(name %q) = %v, want ErrInvalidAccount", name, err)
		}
	}
	if len(f.Outbox.Queued()) != 0 || f.Hasher.Calls() != 0 {
		t.Error("a refused name was mailed or hashed")
	}
	cmd := start("ana@example.edu", "ana")
	cmd.FullName = "Ana-Maria O'Neil Jr."
	begin(t, f, cmd)
}

// The login is mailed to whatever address was typed, so it is kept to what a
// login needs and cannot carry a link or a pitch.
func TestStartRefusesALoginOfMoreThanPlainCharacters(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	for _, login := range []string{
		"Claim your prize at https://evil.example/win now", "https://evil.example", "evil.example", "ana.popescu",
		"ana popescu", "ana@example.edu", "a/b", "ana:1", "-ana", "_ana", "an", strings.Repeat("a", 65), "ană",
	} {
		if err := startErr(t, f, start("ana@example.edu", login)); !errors.Is(err, users.ErrInvalidAccount) {
			t.Errorf("Start(login %q) = %v, want ErrInvalidAccount", login, err)
		}
	}
	if len(f.Store.Rows()) != 0 || len(f.Outbox.Queued()) != 0 || f.Hasher.Calls() != 0 {
		t.Error("a refused login was stored, mailed or hashed")
	}

	for _, login := range []string{"ana", "ana_popescu-2", "007", strings.Repeat("a", 64)} {
		begin(t, f, start(login+"@example.edu", login))
	}
}

func TestStartLowercasesTheLogin(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	b := begin(t, f, start("ana@example.edu", "  Ana-Popescu "))

	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Login != "ana-popescu" {
		t.Errorf("pending = %+v, want the login trimmed and lowercased", rows)
	}
	created, err := f.Service.Confirm(t.Context(), b.confirm(b.code))
	if err != nil || created.Login != "ana-popescu" {
		t.Errorf("Confirm() = %q, %v; want the lowercased login", created.Login, err)
	}
}

func TestStartRefusesATakenLogin(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	f.Users.Add(users.User{Login: "Ana", FullName: "Ana", PasswordHash: "x"})

	if err := startErr(t, f, start("ana@example.edu", "ana")); !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("Start() with an account's login = %v, want ErrLoginTaken", err)
	}

	begin(t, f, start("first@example.edu", "maria"))
	if err := startErr(t, f, start("second@example.edu", "MARIA")); !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("Start() with a pending sign-up's login = %v, want ErrLoginTaken", err)
	}
	if hashes := f.Hasher.Calls(); hashes != 1 {
		t.Errorf("spent %d hashes, want only the accepted sign-up's: a taken login is refused before hashing", hashes)
	}

	f.Clock.Advance(signup.CodeLifetime)
	begin(t, f, start("second@example.edu", "maria"))
	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Email != "second@example.edu" {
		t.Errorf("pending = %+v, want the expired holder replaced by the new sign-up", rows)
	}
}

func TestStartLowercasesTheEmail(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	begin(t, f, start("  Ana@Example.EDU ", "ana"))

	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Email != "ana@example.edu" {
		t.Errorf("pending = %+v, want the email trimmed and lowercased", rows)
	}
	if to := f.Outbox.Queued()[0].To; to != "ana@example.edu" {
		t.Errorf("code sent to %q, want the lowercased address", to)
	}
}

func TestStartAgainReplacesThePendingSignUp(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	first := begin(t, f, start("ana@example.edu", "ana"))

	second := begin(t, f, start("ana@example.edu", "ana2"))

	rows := f.Store.Rows()
	if len(rows) != 1 || rows[0].Login != "ana2" {
		t.Fatalf("pending = %+v, want one row with the second form", rows)
	}
	if first.code != second.code {
		if _, err := f.Service.Confirm(t.Context(), second.confirm(first.code)); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("the first code still confirms: %v", err)
		}
	}
}

func TestStartRefusesWhatAnAccountWouldRefuse(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*signup.StartCommand)
		want error
	}{
		"no email":       {func(c *signup.StartCommand) { c.Email = " " }, users.ErrInvalidAccount},
		"not an email":   {func(c *signup.StartCommand) { c.Email = "Ana <ana@example.edu>" }, users.ErrInvalidAccount},
		"long email":     {func(c *signup.StartCommand) { c.Email = strings.Repeat("a", 250) + "@example.edu" }, users.ErrInvalidAccount},
		"no login":       {func(c *signup.StartCommand) { c.Login = "" }, users.ErrInvalidAccount},
		"long login":     {func(c *signup.StartCommand) { c.Login = strings.Repeat("a", users.MaxLoginLength+1) }, users.ErrInvalidAccount},
		"no name":        {func(c *signup.StartCommand) { c.FullName = "  " }, users.ErrInvalidAccount},
		"long name":      {func(c *signup.StartCommand) { c.FullName = strings.Repeat("a", users.MaxFullNameLength+1) }, users.ErrInvalidAccount},
		"short password": {func(c *signup.StartCommand) { c.Password = "short" }, users.ErrWeakPassword},
		"huge password":  {func(c *signup.StartCommand) { c.Password = strings.Repeat("a", 2000) }, users.ErrWeakPassword},
	} {
		t.Run(name, func(t *testing.T) {
			f := signuptest.NewFixture(t, nil)
			cmd := start("ana@example.edu", "ana")
			tc.edit(&cmd)

			res, err := f.Service.Start(t.Context(), cmd)
			if !errors.Is(err, tc.want) {
				t.Errorf("Start() = %v, want %v", err, tc.want)
			}
			if res.Token != "" || len(f.Store.Rows()) != 0 || len(f.Outbox.Queued()) != 0 || f.Hasher.Calls() != 0 {
				t.Error("a refused form was answered a token, or stored, mailed or hashed something")
			}
		})
	}
}

func TestTheCodeIsWrittenInTheReadersLanguage(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	cmd := start("ana@example.edu", "ana")
	cmd.Lang = "ro"
	begin(t, f, cmd)
	cmd = start("maria@example.edu", "maria")
	cmd.Lang = "xx"
	begin(t, f, cmd)

	queued := f.Outbox.Queued()
	if !strings.Contains(queued[0].Subject, "Codul de înregistrare") {
		t.Errorf("subject %q, want the Romanian one", queued[0].Subject)
	}
	if !strings.Contains(queued[1].Subject, "sign-up code") {
		t.Errorf("subject %q, want the installation's language for an unknown one", queued[1].Subject)
	}
}

func TestConfirmCreatesAVerifiedStudentAndSpendsTheRow(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	f.Clock.Advance(time.Minute)

	cmd := b.confirm(b.code)
	cmd.Email = " ANA@example.edu"
	created, err := f.Service.Confirm(t.Context(), cmd)
	if err != nil {
		t.Fatalf("Confirm() = %v", err)
	}

	stored, ok := f.Users.Get(created.ID)
	if !ok {
		t.Fatal("the account was not stored")
	}
	if stored.Login != "ana" || stored.Email != "ana@example.edu" || stored.FullName != "Ana Popescu" {
		t.Errorf("account = %+v, want the pending sign-up's details", stored)
	}
	if stored.EmailVerifiedAt == nil || !stored.EmailVerifiedAt.Equal(f.Clock.Now()) {
		t.Errorf("email verified at %v, want the confirmation's time %v", stored.EmailVerifiedAt, f.Clock.Now())
	}
	if stored.MustChangePassword || !passwordtest.Matches(t, stored.PasswordHash, chosenPassword) {
		t.Error("the account should sign in with the chosen password, without changing it")
	}
	if len(stored.Roles) != 1 || stored.Roles[0] != users.RoleStudent {
		t.Errorf("roles = %v, want only %s", stored.Roles, users.RoleStudent)
	}
	if rows := f.Store.Rows(); len(rows) != 0 {
		t.Errorf("pending = %+v, want the row spent", rows)
	}

	if _, err := f.Service.Confirm(t.Context(), b.confirm(b.code)); !errors.Is(err, signup.ErrWrongCode) {
		t.Errorf("a second Confirm() = %v, want ErrWrongCode", err)
	}
}

func TestAWrongCodeCountsAndTheSixthTryIsExpired(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))

	for try := 1; try <= signup.MaxTries; try++ {
		if _, err := f.Service.Confirm(t.Context(), b.confirm(otherCode(b.code))); !errors.Is(err, signup.ErrWrongCode) {
			t.Fatalf("try %d with a wrong code = %v, want ErrWrongCode", try, err)
		}
		if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Tries != try {
			t.Fatalf("after try %d the row holds %+v, want the try counted", try, rows)
		}
	}

	if _, err := f.Service.Confirm(t.Context(), b.confirm(b.code)); !errors.Is(err, signup.ErrCodeExpired) {
		t.Errorf("the sixth try, with the right code = %v, want ErrCodeExpired", err)
	}
	if _, err := f.Users.ByLogin(t.Context(), "ana"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ByLogin(ana) = %v, want no account", err)
	}
}

func TestAMalformedCodeIsAWrongOneAndCounts(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))

	for _, code := range []string{"", "12345", "1234567", "12a456", strings.Repeat("1", 10_000)} {
		if _, err := f.Service.Confirm(t.Context(), b.confirm(code)); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("Confirm(%.8q) = %v, want ErrWrongCode", code, err)
		}
	}
	if rows := f.Store.Rows(); rows[0].Tries != signup.MaxTries {
		t.Errorf("tries = %d, want every malformed code counted", rows[0].Tries)
	}
}

// An address with no row is one nobody started a sign-up for lately, known
// or not: the answer is a wrong code either way.
func TestConfirmOfAnEmailWithNoSignUpIsAWrongCode(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	f.Users.Add(users.User{Login: "ana", Email: "ana@example.edu", FullName: "Ana", PasswordHash: "x"})

	for _, email := range []string{"ana@example.edu", "nobody@example.edu", strings.Repeat("a", 300)} {
		if _, err := f.Service.Confirm(t.Context(), signup.ConfirmCommand{Email: email, Code: "123456"}); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("Confirm(%.20q) = %v, want ErrWrongCode, whether or not an account holds the address", email, err)
		}
	}
}

func TestACodeExpiresAfterThirtyMinutes(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	late := begin(t, f, start("late@example.edu", "late"))
	f.Clock.Advance(signup.CodeLifetime - time.Second)
	onTime := begin(t, f, start("ontime@example.edu", "ontime"))
	f.Clock.Advance(time.Second)

	if _, err := f.Service.Confirm(t.Context(), late.confirm(late.code)); !errors.Is(err, signup.ErrCodeExpired) {
		t.Errorf("Confirm() thirty minutes on = %v, want ErrCodeExpired", err)
	}
	if _, err := f.Service.Confirm(t.Context(), onTime.confirm(onTime.code)); err != nil {
		t.Errorf("Confirm() a second on = %v, want the account", err)
	}
	if signup.CodeLifetime != 30*time.Minute {
		t.Errorf("CodeLifetime = %s, want 30m", signup.CodeLifetime)
	}
}

func TestConfirmAfterTheLoginWasTakenCreatesNothing(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	taken := f.Users.Add(users.User{Login: "ANA", FullName: "Made by an administrator", PasswordHash: "x"})

	_, err := f.Service.Confirm(t.Context(), b.confirm(b.code))
	if !errors.Is(err, users.ErrLoginTaken) {
		t.Fatalf("Confirm() = %v, want ErrLoginTaken", err)
	}

	if found, _ := f.Users.ByLogin(t.Context(), "ana"); found.ID != taken.ID {
		t.Errorf("login ana resolves to %v, want only the administrator's account", found.ID)
	}
	if rows := f.Store.Rows(); len(rows) != 0 {
		t.Errorf("pending = %+v, want the row spent", rows)
	}
}

func TestConfirmAfterTheEmailWasTakenCreatesNothing(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	f.Users.Add(users.User{Login: "someone", Email: "Ana@example.edu", FullName: "Someone", PasswordHash: "x"})

	_, err := f.Service.Confirm(t.Context(), b.confirm(b.code))
	if !errors.Is(err, users.ErrEmailTaken) {
		t.Fatalf("Confirm() = %v, want ErrEmailTaken", err)
	}
	if _, err := f.Users.ByLogin(t.Context(), "ana"); !errors.Is(err, users.ErrNotFound) {
		t.Errorf("ByLogin(ana) = %v, want no account", err)
	}
	if rows := f.Store.Rows(); len(rows) != 0 {
		t.Errorf("pending = %+v, want the row spent", rows)
	}
}

func TestResendWaitsSixtySeconds(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))

	if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrTooSoon) {
		t.Errorf("Resend() at once = %v, want ErrTooSoon", err)
	}
	f.Clock.Advance(signup.ResendCooldown - time.Second)
	if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrTooSoon) {
		t.Errorf("Resend() a second early = %v, want ErrTooSoon", err)
	}
	if n := len(f.Outbox.Queued()); n != 1 {
		t.Fatalf("queued %d messages during the cooldown, want only the first code", n)
	}

	f.Clock.Advance(time.Second)
	cmd := b.resend()
	cmd.Email = " ANA@example.edu"
	if err := f.Service.Resend(t.Context(), cmd); err != nil {
		t.Fatalf("Resend() after the cooldown = %v", err)
	}
	queued := f.Outbox.Queued()
	if len(queued) != 2 {
		t.Fatalf("queued %d messages, want a second code", len(queued))
	}
	second := codeIn(t, queued[1])
	if p := f.Store.Rows()[0]; !p.ExpiresAt.Equal(f.Clock.Now().Add(signup.CodeLifetime)) {
		t.Errorf("a resent code expires %v, want %s from now", p.ExpiresAt, signup.CodeLifetime)
	}

	if b.code != second {
		if _, err := f.Service.Confirm(t.Context(), b.confirm(b.code)); !errors.Is(err, signup.ErrWrongCode) {
			t.Errorf("the replaced code = %v, want ErrWrongCode", err)
		}
	}
	if _, err := f.Service.Confirm(t.Context(), b.confirm(second)); err != nil {
		t.Errorf("the resent code = %v, want the account", err)
	}
	if signup.ResendCooldown != 60*time.Second {
		t.Errorf("ResendCooldown = %s, want 60s", signup.ResendCooldown)
	}
}

func TestResendOfAnEmailWithNoSignUpSendsNothing(t *testing.T) {
	f := signuptest.NewFixture(t, nil)

	if err := f.Service.Resend(t.Context(), signup.ResendCommand{Email: "nobody@example.edu", Lang: "en"}); err != nil {
		t.Errorf("Resend() = %v, want nil", err)
	}
	if n := len(f.Outbox.Queued()); n != 0 {
		t.Errorf("queued %d messages for an address with no sign-up", n)
	}
}

func TestResendOfAnExpiredCodeSaysToStartAgain(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	b := begin(t, f, start("ana@example.edu", "ana"))
	f.Clock.Advance(signup.CodeLifetime)

	if err := f.Service.Resend(t.Context(), b.resend()); !errors.Is(err, signup.ErrCodeExpired) {
		t.Errorf("Resend() = %v, want ErrCodeExpired", err)
	}
}

func TestStartRefusesPastTenThousandPending(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	f.Users.Add(users.User{Login: "owner", Email: "owner@example.edu", FullName: "Owner", PasswordHash: "x"})
	f.Store.Fill(signup.MaxPending, f.Clock.Now().Add(signup.CodeLifetime))

	if err := startErr(t, f, start("ana@example.edu", "ana")); !errors.Is(err, signup.ErrFull) {
		t.Errorf("Start() past the cap = %v, want ErrFull", err)
	}
	if err := startErr(t, f, start("owner@example.edu", "ana")); !errors.Is(err, signup.ErrFull) {
		t.Errorf("Start() for a known email past the cap = %v, want the ErrFull a new one gets", err)
	}
	if f.Hasher.Calls() != 0 {
		t.Error("a full store still spent a password hash")
	}

	f.Clock.Advance(signup.CodeLifetime)
	begin(t, f, start("ana@example.edu", "ana"))
	if signup.MaxPending != 10_000 {
		t.Errorf("MaxPending = %d, want 10 000", signup.MaxPending)
	}
}

func TestSweepRemovesExpiredRows(t *testing.T) {
	f := signuptest.NewFixture(t, nil)
	begin(t, f, start("old@example.edu", "old"))
	f.Clock.Advance(10 * time.Minute)
	begin(t, f, start("new@example.edu", "new"))
	f.Clock.Advance(signup.CodeLifetime - 10*time.Minute)

	swept, err := f.Service.Sweep(t.Context(), f.Clock.Now())
	if err != nil {
		t.Fatalf("Sweep() = %v", err)
	}

	if swept != 1 {
		t.Errorf("swept %d rows, want the expired one", swept)
	}
	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Email != "new@example.edu" {
		t.Errorf("pending = %+v, want only the live sign-up", rows)
	}
}

func TestSignupIsOfferedOnlyWithTheSwitchAndMailOn(t *testing.T) {
	for _, c := range []struct {
		name          string
		enabled, mail bool
	}{
		{"both on", true, true},
		{"switch off", false, true},
		{"mail off", true, false},
	} {
		f := signuptest.NewFixture(t, func(cfg *signup.Config) { cfg.Enabled = c.enabled })
		f.Outbox.Off = !c.mail
		if got, want := f.Service.Offered(), c.enabled && c.mail; got != want {
			t.Errorf("%s: Offered() = %v, want %v", c.name, got, want)
		}
	}
}

func TestStartIsNotAvailableWithMailOffOrTheSwitchOff(t *testing.T) {
	for name, setup := range map[string]func(*signuptest.Fixture){
		"switch off": nil,
		"mail off":   func(f *signuptest.Fixture) { f.Outbox.Off = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := signuptest.NewFixture(t, func(c *signup.Config) { c.Enabled = name != "switch off" })
			if setup != nil {
				setup(f)
			}

			if err := startErr(t, f, start("ana@example.edu", "ana")); !errors.Is(err, signup.ErrNotAvailable) {
				t.Errorf("Start() = %v, want ErrNotAvailable", err)
			}
			if _, err := f.Service.Confirm(t.Context(), signup.ConfirmCommand{Email: "ana@example.edu", Code: "123456"}); !errors.Is(err, signup.ErrNotAvailable) {
				t.Errorf("Confirm() = %v, want ErrNotAvailable", err)
			}
			if err := f.Service.Resend(t.Context(), signup.ResendCommand{Email: "ana@example.edu", Lang: "en"}); !errors.Is(err, signup.ErrNotAvailable) {
				t.Errorf("Resend() = %v, want ErrNotAvailable", err)
			}
			if len(f.Store.Rows()) != 0 || f.Hasher.Calls() != 0 {
				t.Error("an unavailable sign-up stored or hashed something")
			}
		})
	}
}
