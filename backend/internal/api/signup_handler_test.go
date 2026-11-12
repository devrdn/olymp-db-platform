package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/signup"
	"github.com/devrdn/db-contest/backend/internal/signup/signuptest"
	"github.com/devrdn/db-contest/backend/internal/users"
)

const signupPassword = "a long enough chosen password"

// signupCookieName is the sign-up cookie's name under the writer the test
// router uses.
var signupCookieName = auth.NewCookieWriter(false).SignupName()

type signupFixture struct {
	*signuptest.Fixture
	router   http.Handler
	sessions *auth.SessionStore
}

func newSignupFixture(t *testing.T) *signupFixture {
	t.Helper()
	return mountSignup(t, signuptest.NewFixture(t, nil))
}

// mountSignup serves the sign-up routes of f's service, which decides at
// mount whether they exist.
func mountSignup(t *testing.T, f *signuptest.Fixture) *signupFixture {
	t.Helper()
	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	limiter := auth.NewLimiter(c)
	service := auth.NewService(auth.ServiceConfig{
		Users: f.Users, Sessions: sessions,
		Audit: audit.New(&apiSink{}), Limiter: limiter, Logger: log,
		Passwords: passwordtest.NewHasher(),
		Devices:   devices(t),
	})
	cookies := auth.NewCookieWriter(false)
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: f.Users,
		Authorizer: rbac.New(noRoles{}), Cookies: cookies, Logger: log,
	})

	router := chi.NewRouter()
	api.NewSignupHandler(f.Service, service, limiter, mw, cookies, log).Mount(router)
	return &signupFixture{Fixture: f, router: router, sessions: sessions}
}

func signupForm(fullName, login, email, pw string) string {
	body, _ := json.Marshal(map[string]string{
		"full_name": fullName, "login": login, "email": email, "password": pw,
	})
	return string(body)
}

func (f *signupFixture) post(from, path, body string, header http.Header, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, values := range header {
		req.Header[key] = values
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.RemoteAddr = from + ":4000"
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *signupFixture) start(from, email, login string) *httptest.ResponseRecorder {
	return f.post(from, "/public/signup", signupForm("Ana Popescu", login, email, signupPassword), nil)
}

func (f *signupFixture) confirm(from, email, code string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": email, "code": code})
	return f.post(from, "/public/signup/confirm", string(body), nil, cookies...)
}

func (f *signupFixture) resend(from, email string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"email": email})
	return f.post(from, "/public/signup/resend", string(body), nil, cookies...)
}

var signupCodePattern = regexp.MustCompile(`\b\d{6}\b`)

// lastCode reads the code out of the last message queued, which must carry
// one.
func (f *signupFixture) lastCode(t *testing.T) string {
	t.Helper()
	queued := f.Outbox.Queued()
	if len(queued) == 0 {
		t.Fatal("nothing was queued")
	}
	last := queued[len(queued)-1]
	if last.Kind != mailing.KindSignupCode {
		t.Fatalf("the last message is %s, want %s", last.Kind, mailing.KindSignupCode)
	}
	code := signupCodePattern.FindString(last.Body)
	if code == "" {
		t.Fatalf("no six-digit code in the body:\n%s", last.Body)
	}
	return code
}

// begin starts a sign-up from 192.0.2.1 and returns the browser's cookie and
// the code mailed.
func (f *signupFixture) begin(t *testing.T, email, login string) (*http.Cookie, string) {
	t.Helper()
	rec := f.start("192.0.2.1", email, login)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	return signupCookieOf(t, rec), f.lastCode(t)
}

func signupCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	if c := responseCookie(rec, signupCookieName); c != nil {
		return c
	}
	t.Fatalf("no %s cookie was set; headers: %v", signupCookieName, rec.Header())
	return nil
}

func cookieNames(rec *httptest.ResponseRecorder) []string {
	var names []string
	for _, c := range rec.Result().Cookies() {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	return names
}

func assertNoStore(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s: Cache-Control = %q, want no-store", name, got)
	}
}

func TestSignupAnswersTheSameForANewAndAKnownEmail(t *testing.T) {
	fresh := newSignupFixture(t)
	known := newSignupFixture(t)
	known.Users.Add(users.User{Login: "owner", Email: "ana@example.edu", FullName: "Ana Owner", PasswordHash: "x"})

	a := fresh.start("192.0.2.1", "Ana@example.edu", "ana")
	b := known.start("192.0.2.1", "Ana@example.edu", "ana")

	if a.Code != http.StatusAccepted || b.Code != http.StatusAccepted {
		t.Fatalf("status new = %d, known = %d, want 202 for both (%s | %s)", a.Code, b.Code, a.Body.String(), b.Body.String())
	}
	if want := `{"status":"code_sent","email":"ana@example.edu"}`; strings.TrimSpace(a.Body.String()) != want {
		t.Errorf("body = %s, want %s", a.Body.String(), want)
	}
	if a.Body.String() != b.Body.String() {
		t.Errorf("known body = %s, want the new one's bytes %s", b.Body.String(), a.Body.String())
	}
	if an, bn := cookieNames(a), cookieNames(b); !slices.Equal(an, bn) || !slices.Equal(an, []string{signupCookieName}) {
		t.Errorf("cookies new = %v, known = %v, want only %s for both", an, bn, signupCookieName)
	}
	assertNoStore(t, "new", a)
	assertNoStore(t, "known", b)
}

func TestStartKeepsTheTokenInACookieAndOutOfEveryBody(t *testing.T) {
	f := newSignupFixture(t)

	rec := f.start("192.0.2.1", "ana@example.edu", "ana")

	cookie := signupCookieOf(t, rec)
	if cookie.Value == "" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Errorf("cookie = %+v, want the token, HttpOnly, SameSite=Lax, Path=/", cookie)
	}
	if want := int(signup.CodeLifetime.Seconds()); cookie.MaxAge != want {
		t.Errorf("MaxAge = %d, want %d", cookie.MaxAge, want)
	}
	f.Clock.Advance(signup.ResendCooldown)
	bodies := map[string]*httptest.ResponseRecorder{
		"start":   rec,
		"confirm": f.confirm("192.0.2.1", "ana@example.edu", "000000", cookie),
		"resend":  f.resend("192.0.2.1", "ana@example.edu", cookie),
	}
	for name, r := range bodies {
		if strings.Contains(r.Body.String(), cookie.Value) {
			t.Errorf("the %s body carries the sign-up token: %s", name, r.Body.String())
		}
	}
}

func TestConfirmSetsTheSessionCookie(t *testing.T) {
	f := newSignupFixture(t)
	signupCookie, code := f.begin(t, "ana@example.edu", "ana")

	rec := f.confirm("192.0.2.1", " Ana@example.edu", code, signupCookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if want := `{"must_change_password":false}`; strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("body = %s, want %s", rec.Body.String(), want)
	}
	assertNoStore(t, "confirm", rec)

	account, err := f.Users.ByLogin(t.Context(), "ana")
	if err != nil {
		t.Fatalf("the account was not created: %v", err)
	}
	session := responseCookie(rec, sessionName)
	if session == nil || session.Value == "" {
		t.Fatalf("no session cookie was set; headers: %v", rec.Header())
	}
	principal, err := f.sessions.Get(t.Context(), session.Value)
	if err != nil || principal.UserID != account.ID {
		t.Errorf("the session = %+v, %v, want one for the new account %s", principal, err, account.ID)
	}
	if device := responseCookie(rec, auth.DeviceCookieName); device == nil || device.Value == "" {
		t.Error("no device cookie was set")
	}
	if cleared := responseCookie(rec, signupCookieName); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("sign-up cookie = %+v, want it cleared", cleared)
	}
	if strings.Contains(rec.Body.String(), session.Value) {
		t.Errorf("the body carries the session token: %s", rec.Body.String())
	}
}

func TestConfirmWithoutTheSignupCookieIsAWrongCodeAndCounts(t *testing.T) {
	f := newSignupFixture(t)
	_, code := f.begin(t, "ana@example.edu", "ana")

	rec := f.confirm("192.0.2.1", "ana@example.edu", code)

	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "signup_wrong_code" {
		t.Errorf("= %d %q, want 400 signup_wrong_code", rec.Code, errorCode(t, rec))
	}
	if rows := f.Store.Rows(); len(rows) != 1 || rows[0].Tries != 1 {
		t.Errorf("pending = %+v, want the try counted", rows)
	}
}

// At that point the sign-up is spent; saying the address is taken would tell
// whoever holds the code that it has an account.
func TestConfirmAfterAnAccountTookTheEmailAnswersAsAnExpiredCode(t *testing.T) {
	taken := newSignupFixture(t)
	takenCookie, takenCode := taken.begin(t, "ana@example.edu", "ana")
	taken.Users.Add(users.User{Login: "someone", Email: "ana@example.edu", FullName: "Someone", PasswordHash: "x"})

	expired := newSignupFixture(t)
	expiredCookie, expiredCode := expired.begin(t, "ana@example.edu", "ana")
	expired.Clock.Advance(signup.CodeLifetime)

	a := taken.confirm("192.0.2.1", "ana@example.edu", takenCode, takenCookie)
	b := expired.confirm("192.0.2.1", "ana@example.edu", expiredCode, expiredCookie)

	if a.Code != http.StatusGone || errorCode(t, a) != "signup_code_expired" {
		t.Errorf("taken = %d %q, want 410 signup_code_expired", a.Code, errorCode(t, a))
	}
	if a.Body.String() != b.Body.String() || a.Code != b.Code {
		t.Errorf("taken = %d %s, want the expired answer's %d %s", a.Code, a.Body.String(), b.Code, b.Body.String())
	}
}

func TestResendMailsANewCodeAndAnswersAsStartDoes(t *testing.T) {
	f := newSignupFixture(t)
	started := f.start("192.0.2.1", "ana@example.edu", "ana")
	cookie := signupCookieOf(t, started)
	first := f.lastCode(t)
	f.Clock.Advance(signup.ResendCooldown)

	rec := f.resend("192.0.2.1", "ANA@example.edu", cookie)

	if rec.Code != http.StatusAccepted || rec.Body.String() != started.Body.String() {
		t.Fatalf("= %d %s, want 202 %s", rec.Code, rec.Body.String(), started.Body.String())
	}
	if len(f.Outbox.Queued()) != 2 || f.lastCode(t) == "" {
		t.Errorf("queued %d messages, want a second code after %s", len(f.Outbox.Queued()), first)
	}
	// The new code opens a new window, so the browser keeps the token as long.
	if renewed := responseCookie(rec, signupCookieName); renewed == nil || renewed.Value != cookie.Value ||
		renewed.MaxAge != int(signup.CodeLifetime.Seconds()) {
		t.Errorf("sign-up cookie = %+v, want the same token for another %v", renewed, signup.CodeLifetime)
	}

	again := f.resend("192.0.2.1", "ana@example.edu", cookie)
	if again.Code != http.StatusTooManyRequests || errorCode(t, again) != "signup_too_soon" || again.Header().Get("Retry-After") != "60" {
		t.Errorf("= %d %q Retry-After %q, want 429 signup_too_soon after 60", again.Code, errorCode(t, again), again.Header().Get("Retry-After"))
	}
	assertNoStore(t, "too soon", again)
}

func TestStartAnswersItsRefusals(t *testing.T) {
	cases := []struct {
		name, login, pw string
		setup           func(*signupFixture)
		status          int
		code            string
	}{
		{"taken login", "ana", signupPassword, func(f *signupFixture) {
			f.Users.Add(users.User{Login: "ana", FullName: "Ana", PasswordHash: "x"})
		}, http.StatusConflict, "login_taken"},
		{"weak password", "ana", "short", nil, http.StatusBadRequest, "weak_password"},
		{"login with a dot", "a.na", signupPassword, nil, http.StatusBadRequest, "invalid_request"},
		{"full", "ana", signupPassword, func(f *signupFixture) {
			f.Store.Fill(signup.MaxPending, f.Clock.Now().Add(time.Hour))
		}, http.StatusServiceUnavailable, "signup_full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSignupFixture(t)
			if c.setup != nil {
				c.setup(f)
			}
			rec := f.post("192.0.2.1", "/public/signup", signupForm("Ana Popescu", c.login, "ana@example.edu", c.pw), nil)
			if rec.Code != c.status || errorCode(t, rec) != c.code {
				t.Errorf("= %d %q, want %d %q", rec.Code, errorCode(t, rec), c.status, c.code)
			}
			if responseCookie(rec, signupCookieName) != nil {
				t.Error("a refused start set the sign-up cookie")
			}
			assertNoStore(t, c.name, rec)
		})
	}
}

// Every field is bounded before it reaches the service or becomes a cache
// key, so a refused one costs no hash and no counter.
func TestSignupRefusesAnOverlongFieldBeforeTheService(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	longEmail := long(users.MaxEmailLength) + "@example.edu"
	cases := map[string]struct{ path, body string }{
		"full name":     {"/public/signup", signupForm(long(users.MaxFullNameLength+1), "ana", "ana@example.edu", signupPassword)},
		"login":         {"/public/signup", signupForm("Ana Popescu", long(users.MaxLoginLength+1), "ana@example.edu", signupPassword)},
		"email":         {"/public/signup", signupForm("Ana Popescu", "ana", longEmail, signupPassword)},
		"password":      {"/public/signup", signupForm("Ana Popescu", "ana", "ana@example.edu", long(password.MaxLength+1))},
		"code":          {"/public/signup/confirm", `{"email":"ana@example.edu","code":"` + long(signup.CodeDigits+1) + `"}`},
		"confirm email": {"/public/signup/confirm", `{"email":"` + longEmail + `","code":"123456"}`},
		"resend email":  {"/public/signup/resend", `{"email":"` + longEmail + `"}`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSignupFixture(t)
			// More than one email's budget, each from its own address: none
			// reaches the email limiter.
			for i := range api.SignupsPerEmail + 1 {
				rec := f.post(fmt.Sprintf("192.0.2.%d", i+1), c.path, c.body, nil)
				if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
					t.Fatalf("attempt %d = %d %q, want 400 invalid_request", i, rec.Code, errorCode(t, rec))
				}
			}
			if f.Hasher.Calls() != 0 || len(f.Store.Rows()) != 0 || len(f.Outbox.Queued()) != 0 {
				t.Error("an overlong field reached the service")
			}
		})
	}
}

func TestSignupRoutesDoNotExistWhenTheSwitchIsOff(t *testing.T) {
	cases := map[string]struct {
		edit    func(*signup.Config)
		mailOff bool
		mounted bool
	}{
		"on":         {mounted: true},
		"switch off": {edit: func(c *signup.Config) { c.Enabled = false }},
		"mail off":   {mailOff: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			inner := signuptest.NewFixture(t, c.edit)
			inner.Outbox.Off = c.mailOff
			f := mountSignup(t, inner)

			for path, body := range map[string]string{
				"/public/signup":         signupForm("Ana Popescu", "ana", "ana@example.edu", signupPassword),
				"/public/signup/confirm": `{"email":"ana@example.edu","code":"123456"}`,
				"/public/signup/resend":  `{"email":"ana@example.edu"}`,
			} {
				rec := f.post("192.0.2.1", path, body, nil)
				if c.mounted {
					if rec.Code == http.StatusNotFound {
						t.Errorf("POST %s = 404, want the route served", path)
					}
					continue
				}
				if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "signup_") {
					t.Errorf("POST %s = %d %s, want the router's 404", path, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// signupEndpoint drives one endpoint for the budget tests; admitted is what
// a request the limiter lets through is answered with.
type signupEndpoint struct {
	name     string
	call     func(f *signupFixture, from, email string) *httptest.ResponseRecorder
	admitted int
}

// The start requests are refused for a weak password, so every one is a
// refusal that still spends the budgets (CLAUDE.md rule 13).
var signupEndpoints = []signupEndpoint{
	{"start", func(f *signupFixture, from, email string) *httptest.ResponseRecorder {
		return f.post(from, "/public/signup", signupForm("Ana Popescu", "ana", email, "short"), nil)
	}, http.StatusBadRequest},
	{"confirm", func(f *signupFixture, from, email string) *httptest.ResponseRecorder {
		return f.confirm(from, email, "123456")
	}, http.StatusBadRequest},
	{"resend", func(f *signupFixture, from, email string) *httptest.ResponseRecorder {
		return f.resend(from, email)
	}, http.StatusAccepted},
}

// CLAUDE.md rule 5: a caller refused for its address spends no email budget,
// so it cannot make counters for addresses it invents.
func TestSignupSpendsTheAddressBudgetBeforeTheEmailBudget(t *testing.T) {
	for _, e := range signupEndpoints {
		t.Run(e.name, func(t *testing.T) {
			f := newSignupFixture(t)
			for i := range api.SignupsPerAddress {
				if rec := e.call(f, "192.0.2.1", fmt.Sprintf("filler%d@example.edu", i)); rec.Code != e.admitted {
					t.Fatalf("filler %d = %d, want %d (%s)", i, rec.Code, e.admitted, rec.Body.String())
				}
			}
			for range api.SignupsPerEmail {
				rec := e.call(f, "192.0.2.1", "target@example.edu")
				if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
					t.Fatalf("over the address budget = %d %q, want 429 too_many_attempts", rec.Code, errorCode(t, rec))
				}
				assertNoStore(t, "refused", rec)
			}

			if rec := e.call(f, "192.0.2.2", "target@example.edu"); rec.Code != e.admitted {
				t.Errorf("the email from a fresh address = %d, want %d: refusals by address spent its budget", rec.Code, e.admitted)
			}
		})
	}
}

func TestEachSignupEndpointLimitsAnEmailWhateverItsCase(t *testing.T) {
	for _, e := range signupEndpoints {
		t.Run(e.name, func(t *testing.T) {
			f := newSignupFixture(t)
			for i := range api.SignupsPerEmail {
				if rec := e.call(f, fmt.Sprintf("192.0.2.%d", i+1), "Ana@Example.edu"); rec.Code != e.admitted {
					t.Fatalf("attempt %d = %d, want %d", i, rec.Code, e.admitted)
				}
			}

			rec := e.call(f, "192.0.2.100", " ana@example.EDU")
			if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
				t.Errorf("= %d %q, want 429 too_many_attempts", rec.Code, errorCode(t, rec))
			}

			// The other endpoints keep their own budgets for the email and the
			// address.
			for _, other := range signupEndpoints {
				if other.name == e.name {
					continue
				}
				if rec := other.call(f, "192.0.2.100", "ana@example.edu"); rec.Code != other.admitted {
					t.Errorf("%s after %s spent its budget = %d, want %d", other.name, e.name, rec.Code, other.admitted)
				}
			}
		})
	}
}

func TestTheCodeIsMailedInTheLanguageTheReaderAsksFor(t *testing.T) {
	texts := mailing.NewTexts("https://contest.example.edu")
	subject := func(t *testing.T, lang mailing.Language) string {
		t.Helper()
		text, err := texts.SignupCode(lang, mailing.SignupCodeData{
			Installation: "DB Contest", FullName: "Ana Popescu", Login: "ana", Code: "123456",
		})
		if err != nil {
			t.Fatalf("SignupCode(%s) = %v", lang, err)
		}
		return text.Subject
	}
	cases := map[string]struct {
		header http.Header
		path   string
		want   mailing.Language
	}{
		"a language the texts are written in": {http.Header{"Accept-Language": {"de;q=0.9, ro-RO;q=0.8"}}, "/public/signup", mailing.Romanian},
		"the query first":                     {http.Header{"Accept-Language": {"ro"}}, "/public/signup?lang=en", mailing.English},
		"one they are not":                    {http.Header{"Accept-Language": {"de"}}, "/public/signup", mailing.Russian},
		"none":                                {nil, "/public/signup", mailing.Russian},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := mountSignup(t, signuptest.NewFixture(t, func(cfg *signup.Config) {
				cfg.Settings = signuptest.Settings{
					settings.KeyName: "DB Contest", settings.KeyMailLanguage: string(mailing.Russian),
				}
			}))

			rec := f.post("192.0.2.1", c.path, signupForm("Ana Popescu", "ana", "ana@example.edu", signupPassword), c.header)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("start = %d, want 202 (%s)", rec.Code, rec.Body.String())
			}
			queued := f.Outbox.Queued()
			if got, want := queued[len(queued)-1].Subject, subject(t, c.want); got != want {
				t.Errorf("subject = %q, want %q (%s)", got, want, c.want)
			}
		})
	}
}
