package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/recovery"
	"github.com/devrdn/db-contest/backend/internal/recovery/recoverytest"
	"github.com/devrdn/db-contest/backend/internal/users"
)

const recoveryPassword = "a long enough new password"

type recoveryFixture struct {
	*recoverytest.Fixture
	router http.Handler
	cache  *cache.Memory
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	return mountRecovery(t, recoverytest.NewFixture(t, nil))
}

// mountRecovery serves the recovery routes of f's service, which decides at
// mount whether they exist.
func mountRecovery(t *testing.T, f *recoverytest.Fixture) *recoveryFixture {
	t.Helper()
	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	router := chi.NewRouter()
	api.NewRecoveryHandler(f.Service, auth.NewLimiter(c), logging.New("error", io.Discard)).Mount(router)
	return &recoveryFixture{Fixture: f, router: router, cache: c}
}

func (f *recoveryFixture) post(from, path, body string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, values := range header {
		req.Header[key] = values
	}
	req.RemoteAddr = from + ":4000"
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *recoveryFixture) forgot(from, login string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"login": login})
	return f.post(from, "/public/password/forgot", string(body), nil)
}

func (f *recoveryFixture) reset(from, token, pw string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token, "password": pw})
	return f.post(from, "/public/password/reset", string(body), nil)
}

func (f *recoveryFixture) student(login, email string) users.User {
	return f.Users.Add(users.User{
		Login: login, Email: email, FullName: "Ana Popescu", Roles: []string{users.RoleStudent}, PasswordHash: "x",
	})
}

var recoveryTokenPattern = regexp.MustCompile(regexp.QuoteMeta(recovery.ResetPath) + `([A-Za-z0-9_-]+)`)

// settle waits for every request handed to the background to finish; the
// service takes no more afterwards.
func (f *recoveryFixture) settle(t *testing.T) {
	t.Helper()
	if err := f.Service.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
}

// lastToken reads the token out of the last message queued, which must
// carry a link, waiting for the background request that queues it.
func (f *recoveryFixture) lastToken(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	queued := f.Outbox.Queued()
	for len(queued) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		queued = f.Outbox.Queued()
	}
	if len(queued) == 0 {
		t.Fatal("nothing was queued")
	}
	match := recoveryTokenPattern.FindStringSubmatch(queued[len(queued)-1].Body)
	if match == nil {
		t.Fatalf("no reset link in the last message:\n%s", queued[len(queued)-1].Body)
	}
	return match[1]
}

func TestForgotAnswersTheSameForEveryAccount(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	f.Users.Add(users.User{Login: "organiser", Email: "organiser@example.edu", FullName: "Org",
		Roles: []string{recoverytest.StaffRole}, PasswordHash: "x"})
	f.Users.Add(users.User{Login: "gone", Email: "gone@example.edu", FullName: "Gone", Status: users.StatusDeleted})
	f.Users.Add(users.User{Login: "held", Email: "held@example.edu", FullName: "Held", Status: users.StatusBlocked,
		Roles: []string{users.RoleStudent}, PasswordHash: "x"})
	f.Users.Add(users.User{Login: "mailless", FullName: "No Mail", Roles: []string{users.RoleStudent}, PasswordHash: "x"})

	real := f.forgot("192.0.2.1", "ana@example.edu")
	if real.Code != http.StatusAccepted {
		t.Fatalf("forgot = %d, want 202 (%s)", real.Code, real.Body.String())
	}
	if want := `{"status":"link_sent"}`; strings.TrimSpace(real.Body.String()) != want {
		t.Errorf("body = %s, want %s", real.Body.String(), want)
	}
	assertNoStore(t, "real", real)
	for _, typed := range []string{"nobody", "nobody@example.edu", "organiser", "gone", "held", "held@example.edu", "mailless", ""} {
		rec := f.forgot("192.0.2.1", typed)
		if rec.Code != real.Code || rec.Body.String() != real.Body.String() {
			t.Errorf("forgot(%q) = %d %s, want the real account's %d %s", typed, rec.Code, rec.Body.String(), real.Code, real.Body.String())
		}
		assertNoStore(t, typed, rec)
	}
	f.settle(t)
	if q := f.Outbox.Queued(); len(q) != 1 {
		t.Errorf("queued %d messages, want only the real account's", len(q))
	}
}

func TestResetSetsThePasswordOnceAndAnswersAUsedLink(t *testing.T) {
	f := newRecoveryFixture(t)
	account := f.student("ana", "ana@example.edu")
	f.forgot("192.0.2.1", "ana")
	token := f.lastToken(t)

	rec := f.reset("192.0.2.1", token, recoveryPassword)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("reset = %d %s, want 204 and no body", rec.Code, rec.Body.String())
	}
	assertNoStore(t, "reset", rec)
	if after, _ := f.Users.Get(account.ID); !passwordtest.Matches(t, after.PasswordHash, recoveryPassword) {
		t.Error("the new password does not sign in")
	}

	again := f.reset("192.0.2.1", token, recoveryPassword)
	if again.Code != http.StatusGone || errorCode(t, again) != "reset_link_invalid" {
		t.Errorf("reset again = %d %s, want 410 reset_link_invalid", again.Code, again.Body.String())
	}
	assertNoStore(t, "refused", again)
}

func TestResetAnswersAWeakPassword(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	f.forgot("192.0.2.1", "ana")

	rec := f.reset("192.0.2.1", f.lastToken(t), "short")
	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "weak_password" {
		t.Errorf("reset = %d %s, want 400 weak_password", rec.Code, rec.Body.String())
	}
}

func TestRecoveryRefusesAnOverlongFieldBeforeTheService(t *testing.T) {
	f := newRecoveryFixture(t)
	cases := map[string]*httptest.ResponseRecorder{
		"login":    f.forgot("192.0.2.1", strings.Repeat("a", users.MaxEmailLength+1)),
		"token":    f.reset("192.0.2.1", strings.Repeat("A", recovery.TokenLength+1), recoveryPassword),
		"password": f.reset("192.0.2.1", strings.Repeat("A", recovery.TokenLength), strings.Repeat("p", password.MaxLength+1)),
		// Within every bound once decoded; only the body's size is refused.
		"body": f.post("192.0.2.1", "/public/password/reset",
			fmt.Sprintf(`{"token":"%s","password":"%s"%s}`, strings.Repeat("A", recovery.TokenLength), recoveryPassword,
				strings.Repeat(" ", 16<<10)), nil),
	}
	for name, rec := range cases {
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
			t.Errorf("%s: = %d %s, want 400 invalid_request", name, rec.Code, rec.Body.String())
		}
	}
	if f.Store.Calls() != 0 || f.Hasher.Calls() != 0 {
		t.Errorf("the store was asked %d times and %d passwords hashed, want neither", f.Store.Calls(), f.Hasher.Calls())
	}
}

func TestRecoveryRoutesDoNotExistWhenMailIsOff(t *testing.T) {
	for name, mailOff := range map[string]bool{"on": false, "mail off": true} {
		t.Run(name, func(t *testing.T) {
			inner := recoverytest.NewFixture(t, nil)
			inner.Outbox.Off = mailOff
			f := mountRecovery(t, inner)

			for path, body := range map[string]string{
				"/public/password/forgot": `{"login":"ana"}`,
				"/public/password/reset":  `{"token":"x","password":"a long enough new password"}`,
			} {
				rec := f.post("192.0.2.1", path, body, nil)
				if !mailOff {
					if rec.Code == http.StatusNotFound {
						t.Errorf("POST %s = 404, want the route served", path)
					}
					continue
				}
				if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "reset_") {
					t.Errorf("POST %s = %d %s, want the router's 404", path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// CLAUDE.md rule 5: a caller refused for its address spends no account
// budget, so it cannot make counters for identifiers it invents.
func TestForgotSpendsTheAddressBudgetFirst(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	for i := range api.RecoveriesPerAddress {
		if rec := f.forgot("192.0.2.1", fmt.Sprintf("filler%d", i)); rec.Code != http.StatusAccepted {
			t.Fatalf("filler %d = %d, want 202 (%s)", i, rec.Code, rec.Body.String())
		}
	}
	for range api.RecoveriesPerAccount {
		rec := f.forgot("192.0.2.1", "ana")
		if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
			t.Fatalf("over the address budget = %d %s, want 429 too_many_attempts", rec.Code, rec.Body.String())
		}
		assertNoStore(t, "refused", rec)
	}

	rec := f.forgot("192.0.2.2", "ana")
	f.settle(t)
	if rec.Code != http.StatusAccepted || len(f.Outbox.Queued()) != 1 {
		t.Errorf("the account from a fresh address = %d with %d queued, want 202 and the link: refusals by address spent its budget",
			rec.Code, len(f.Outbox.Queued()))
	}
}

func TestForgotLimitsAnIdentifierWhateverItsCaseAndWhetherItExists(t *testing.T) {
	for _, typed := range [][2]string{{"Ana", " ana "}, {"Nobody@Example.edu", "nobody@example.EDU "}} {
		t.Run(typed[0], func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.student("ana", "ana@example.edu")
			for i := range api.RecoveriesPerAccount {
				if rec := f.forgot(fmt.Sprintf("192.0.2.%d", i+1), typed[0]); rec.Code != http.StatusAccepted {
					t.Fatalf("attempt %d = %d, want 202", i, rec.Code)
				}
			}

			rec := f.forgot("192.0.2.100", typed[1])
			if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
				t.Errorf("= %d %s, want 429 too_many_attempts", rec.Code, rec.Body.String())
			}
			if rec := f.forgot("192.0.2.100", "someone-else"); rec.Code != http.StatusAccepted {
				t.Errorf("another identifier from the same address = %d, want 202", rec.Code)
			}
		})
	}
}

// The reset runs argon2 on what an unauthenticated caller sends, so its
// address budget is spent before the hash, refusals included (CLAUDE.md
// rule 13).
func TestResetSpendsTheAddressBudgetBeforeHashing(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	f.forgot("192.0.2.9", "ana")
	token := f.lastToken(t)
	for i := range api.RecoveriesPerAddress {
		if rec := f.reset("192.0.2.1", strings.Repeat("A", recovery.TokenLength), recoveryPassword); rec.Code != http.StatusGone {
			t.Fatalf("attempt %d = %d, want 410 (%s)", i, rec.Code, rec.Body.String())
		}
	}

	rec := f.reset("192.0.2.1", token, recoveryPassword)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
		t.Fatalf("over the budget = %d %s, want 429 too_many_attempts", rec.Code, rec.Body.String())
	}
	assertNoStore(t, "refused", rec)
	if f.Hasher.Calls() != 0 {
		t.Errorf("spent %d password hashes, want none", f.Hasher.Calls())
	}
	if rec := f.reset("192.0.2.2", token, recoveryPassword); rec.Code != http.StatusNoContent {
		t.Errorf("the link from a fresh address = %d %s, want 204", rec.Code, rec.Body.String())
	}
}

func TestTheLinkIsMailedInTheLanguageTheReaderAsksFor(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	want, err := mailing.NewTexts(recoverytest.PublicURL).PasswordReset(mailing.Romanian, mailing.PasswordResetData{
		Installation: "DB Contest", FullName: "Ana Popescu", Login: "ana", Link: "x",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := f.post("192.0.2.1", "/public/password/forgot", `{"login":"ana"}`, http.Header{"Accept-Language": {"de;q=0.9, ro-RO;q=0.8"}})

	if rec.Code != http.StatusAccepted {
		t.Fatalf("forgot = %d, want 202", rec.Code)
	}
	f.settle(t)
	if queued := f.Outbox.Queued(); len(queued) != 1 || queued[0].Subject != want.Subject {
		t.Errorf("queued %+v, want one message with the subject %q", queued, want.Subject)
	}
}

// gatedRecovery serves a service whose account lookups wait at the gate.
func gatedRecovery(t *testing.T) (*recoveryFixture, *recoverytest.Gate) {
	t.Helper()
	gate := recoverytest.NewGate()
	f := mountRecovery(t, recoverytest.NewFixture(t, func(c *recovery.Config) {
		c.Directory = &recoverytest.GatedDirectory{Directory: c.Directory, Gate: gate}
	}))
	// Registered after the fixture's Stop, so it runs first and Stop can end.
	t.Cleanup(gate.Release)
	return f, gate
}

func TestForgotAnswersWithoutWaitingForTheLookup(t *testing.T) {
	f, gate := gatedRecovery(t)
	f.student("ana", "ana@example.edu")

	rec := f.forgot("192.0.2.1", "ana")

	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"status":"link_sent"}` {
		t.Fatalf("forgot = %d %s, want 202 while the lookup is held", rec.Code, rec.Body.String())
	}
	select {
	case <-gate.Entered():
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the lookup")
	}
	if q := f.Outbox.Queued(); len(q) != 0 {
		t.Fatalf("queued %d messages before the lookup was released", len(q))
	}
	gate.Release()
	f.settle(t)
	if q := f.Outbox.Queued(); len(q) != 1 {
		t.Errorf("queued %d messages, want the link once the lookup finished", len(q))
	}
}

func TestForgotAnswersAlikeWhenTheWorkerIsFull(t *testing.T) {
	f, gate := gatedRecovery(t)
	for i := range recovery.MaxInFlight {
		f.forgot(fmt.Sprintf("192.0.2.%d", i+1), fmt.Sprintf("filler%d", i))
	}
	for range recovery.MaxInFlight {
		select {
		case <-gate.Entered():
		case <-time.After(5 * time.Second):
			t.Fatal("the fillers never reached the lookup")
		}
	}

	rec := f.forgot("192.0.2.100", "ana")

	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != `{"status":"link_sent"}` {
		t.Errorf("forgot with the worker full = %d %s, want the usual 202", rec.Code, rec.Body.String())
	}
	if log := f.Log.String(); !strings.Contains(log, "level=WARN") || !strings.Contains(log, "dropped") {
		t.Errorf("log = %q, want the dropped request at warn", log)
	}
}

func TestForgotAnswers202ForAFailureAfterTheHandOff(t *testing.T) {
	f := newRecoveryFixture(t)
	f.student("ana", "ana@example.edu")
	f.Store.ReplaceErr = errors.New("the database is away")

	rec := f.forgot("192.0.2.1", "ana")
	f.settle(t)

	if rec.Code != http.StatusAccepted {
		t.Errorf("forgot = %d %s, want 202: nobody waits for the outcome", rec.Code, rec.Body.String())
	}
	if log := f.Log.String(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "the database is away") {
		t.Errorf("log = %q, want the failure at error", log)
	}
}

// The recovery budget is spent in the background, only for a link about to
// be mailed, so past it a request is answered as every other one is and the
// link is dropped: a refusal would say the account exists.
func TestAForgotPastTheRecoveryBudgetIsAnsweredAlike(t *testing.T) {
	f := mountRecovery(t, recoverytest.NewFixture(t, func(cfg *recovery.Config) {
		cfg.MailBudget = mailing.NewBudget("recovery", mailing.Limits{PerMinute: 1, PerDay: recovery.MailsPerDay},
			auth.NewLimiter(cache.NewMemory(1000)), cfg.Log)
	}))
	f.student("ana", "ana@example.edu")

	for i := range 3 {
		if rec := f.forgot(fmt.Sprintf("192.0.2.%d", i+1), "ana@example.edu"); rec.Code != http.StatusAccepted {
			t.Fatalf("forgot %d = %d, want 202 (%s)", i, rec.Code, rec.Body.String())
		}
		f.settle(t)
	}

	if queued := f.Outbox.Queued(); len(queued) != 1 {
		t.Errorf("queued %d messages, want the one link the budget admits", len(queued))
	}
}
