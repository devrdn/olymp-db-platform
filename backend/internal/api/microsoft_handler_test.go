package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/microsoft/microsofttest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
)

// Made-up tenants and application; none exists at Microsoft.
const (
	msClientID    = "11111111-2222-4333-8444-555555555555"
	msHomeTenant  = "0a0a0a0a-1b1b-4c2c-8d3d-4e4e4e4e4e4e"
	msOtherTenant = "f0f0f0f0-e1e1-4d2d-9c3c-b4b4b4b4b4b4"
	msObjectID    = "abababab-cdcd-4efe-8a0a-121212121212"
)

var msStateCookieName = auth.NewCookieWriter(false).MicrosoftStateName()

type microsoftFixture struct {
	provider *microsofttest.Provider
	repo     *userstest.Repository
	sessions *auth.SessionStore
	router   http.Handler
}

type microsoftOptions struct {
	createAccounts bool
	noClient       bool
}

func newMicrosoftFixture(t *testing.T, opts microsoftOptions) *microsoftFixture {
	t.Helper()
	p := microsofttest.NewProvider(t)
	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	repo.GrantRole("organizer", "contests.manage")

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	limiter := auth.NewLimiter(c)
	authService := auth.NewService(auth.ServiceConfig{
		Users: repo, Sessions: sessions,
		Audit: audit.New(&apiSink{}), Limiter: limiter, Logger: log,
		Passwords: passwordtest.NewHasher(),
		Devices:   devices(t),
	})
	cookies := auth.NewCookieWriter(false)
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: cookies, Logger: log,
	})
	accounts := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()).
		WithExternalIdentities(repo)

	var client *microsoft.Client
	if !opts.noClient {
		var err error
		client, err = microsoft.New(microsoft.Config{
			ClientID: msClientID, ClientSecret: "a-test-client-secret",
			Tenant: microsoft.TenantCommon, HomeTenant: msHomeTenant,
			RedirectURL: "https://contest.example.edu/api/v1/auth/microsoft/callback",
			BaseURL:     p.URL,
		})
		if err != nil {
			t.Fatalf("microsoft.New() = %v", err)
		}
	}

	router := chi.NewRouter()
	api.NewMicrosoftHandler(api.MicrosoftSignIn{
		Client: client,
		States: microsoft.NewStateSealer([]byte(strings.Repeat("k", 32)), nil),
		Accounts: federation.NewService(federation.Config{
			Policy: federation.Policy{CreateAccounts: opts.createAccounts},
			Store:  repo, Accounts: accounts, PendingLogins: noPendingLogins{},
		}),
		Auth: authService, Limiter: limiter, Middleware: mw, Cookies: cookies, Log: log,
	}).Mount(router)
	return &microsoftFixture{provider: p, repo: repo, sessions: sessions, router: router}
}

type noPendingLogins struct{}

func (noPendingLogins) LoginHeld(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (f *microsoftFixture) get(from, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.RemoteAddr = from + ":4000"
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// started is one sign-in begun at /start: the browser's state cookie and the
// parameters sent to Microsoft.
type started struct {
	cookie *http.Cookie
	query  url.Values
}

func (f *microsoftFixture) start(t *testing.T, from, returnPath string) started {
	t.Helper()
	rec := f.get(from, "/auth/microsoft/start?return="+url.QueryEscape(returnPath))
	if rec.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302 (%s)", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Location %q: %v", rec.Header().Get("Location"), err)
	}
	return started{cookie: cookieNamed(t, rec, msStateCookieName), query: location.Query()}
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", name, rec.Header().Values("Set-Cookie"))
	return nil
}

// signIn has Microsoft send the browser back with a code for a token of
// claims carrying this sign-in's nonce.
func (f *microsoftFixture) signIn(t *testing.T, from string, s started, claims map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	claims["nonce"] = s.query.Get("nonce")
	f.provider.Issue("code-"+s.query.Get("state"), f.provider.Sign(t, claims))
	return f.callback(from, "code=code-"+s.query.Get("state")+"&state="+s.query.Get("state"), s.cookie)
}

func (f *microsoftFixture) callback(from, query string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return f.get(from, "/auth/microsoft/callback?"+query, cookies...)
}

func (f *microsoftFixture) claims(tid string) map[string]any {
	claims := f.provider.Claims(msClientID, tid, msObjectID, "")
	claims["email"] = "ana@example.edu"
	return claims
}

func assertLandsOnSignIn(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error="+code {
		t.Errorf("answered %d to %q, want 302 to /login?error=%s (%s)", rec.Code, rec.Header().Get("Location"), code, rec.Body.String())
	}
	assertStateCleared(t, rec)
}

func assertStateCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == msStateCookieName {
			if c.MaxAge >= 0 || c.Value != "" {
				t.Errorf("state cookie %+v, want it cleared", c)
			}
			return
		}
	}
	t.Error("the callback left the state cookie in place")
}

func sessionOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	return cookieNamed(t, rec, auth.NewCookieWriter(false).SessionName())
}

func TestTheStartSendsTheBrowserToMicrosoftWithASignedState(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{})

	rec := f.get("192.0.2.1", "/auth/microsoft/start?return=/contests/42")

	if rec.Code != http.StatusFound {
		t.Fatalf("start = %d, want 302", rec.Code)
	}
	location, _ := url.Parse(rec.Header().Get("Location"))
	if got := location.Scheme + "://" + location.Host + location.Path; got != f.provider.URL+"/common/oauth2/v2.0/authorize" {
		t.Errorf("sent to %s, want the common authority's authorize endpoint", got)
	}
	q := location.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize parameters %v, want state, nonce and an S256 challenge", q)
	}
	cookie := cookieNamed(t, rec, msStateCookieName)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 600 {
		t.Errorf("state cookie %+v, want HttpOnly, Lax, Path=/, ten minutes", cookie)
	}
	if strings.Contains(cookie.Value, q.Get("code_challenge")) {
		t.Error("the cookie carries the challenge instead of the verifier")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if n := f.provider.Requests(); n != 0 {
		t.Errorf("the start asked Microsoft %d times, want none", n)
	}
}

func TestTheCallbackSignsInAVerifiedToken(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/contests/42?tab=mine")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/contests/42?tab=mine" {
		t.Fatalf("callback = %d to %q, want 302 to the return path (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	assertStateCleared(t, rec)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	principal, err := f.sessions.Get(t.Context(), sessionOf(t, rec).Value)
	if err != nil {
		t.Fatalf("the session cookie names no session: %v", err)
	}
	account, ok := f.repo.Get(principal.UserID)
	if !ok || account.Email != "ana@example.edu" || account.EmailVerifiedAt == nil {
		t.Errorf("signed into %+v, want a new account with the home tenant's verified address", account)
	}
	if ids := f.repo.Identities(principal.UserID); len(ids) != 1 || ids[0].Subject != msObjectID ||
		ids[0].Issuer != f.provider.Issuer(msHomeTenant) {
		t.Errorf("identities %+v, want the token's issuer and object ID", ids)
	}
	if f.provider.LastTokenRequest().Get("code_verifier") == "" {
		t.Error("the exchange sent no PKCE verifier")
	}
}

func TestAForeignTenantClaimingAnAccountsAddressNeverEntersIt(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	s := f.start(t, "192.0.2.1", "/")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msOtherTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d to %q, want a sign-in", rec.Code, rec.Header().Get("Location"))
	}
	principal, err := f.sessions.Get(t.Context(), sessionOf(t, rec).Value)
	if err != nil {
		t.Fatalf("no session: %v", err)
	}
	if principal.UserID == ana.ID {
		t.Fatal("a foreign tenant asserting ana's address entered ana's account")
	}
	if ids := f.repo.Identities(ana.ID); len(ids) != 0 {
		t.Errorf("ana's account was linked: %+v", ids)
	}
	if account, _ := f.repo.Get(principal.UserID); account.Email != "" {
		t.Errorf("the new account holds the untrusted address %q", account.Email)
	}
}

func TestTheCallbackRefusesAStateItDidNotIssue(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")
	other := microsoft.NewStateSealer([]byte(strings.Repeat("o", 32)), nil)
	forged := &http.Cookie{Name: msStateCookieName, Value: other.Seal(microsoft.State{
		State: s.query.Get("state"), Nonce: s.query.Get("nonce"), Verifier: strings.Repeat("v", 43), Return: "/",
	})}
	code := "code=a-code&state=" + s.query.Get("state")

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no cookie":                 f.callback("192.0.2.1", code),
		"another state":             f.callback("192.0.2.1", "code=a-code&state=not-the-state", s.cookie),
		"no state":                  f.callback("192.0.2.1", "code=a-code", s.cookie),
		"a cookie signed elsewhere": f.callback("192.0.2.1", code, forged),
		"no code":                   f.callback("192.0.2.1", "state="+s.query.Get("state"), s.cookie),
		"a provider refusal":        f.callback("192.0.2.1", "error=access_denied&error_description=declined&state="+s.query.Get("state"), s.cookie),
	} {
		t.Run(name, func(t *testing.T) {
			assertLandsOnSignIn(t, rec, "external_signin_failed")
		})
	}
	if n := f.provider.Exchanges(); n != 0 {
		t.Errorf("%d codes were exchanged for refused callbacks, want none", n)
	}
}

func TestARefusedTokenLandsOnTheSignInPage(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")
	claims := f.claims(msHomeTenant)
	claims["aud"] = "66666666-7777-4888-9999-aaaaaaaaaaaa"

	rec := f.signIn(t, "192.0.2.1", s, claims)

	assertLandsOnSignIn(t, rec, "external_signin_failed")
	if f.repo.Len() != 0 {
		t.Errorf("%d accounts exist after a refused token, want none", f.repo.Len())
	}
}

func TestARefusedAccountLandsOnItsCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		create  bool
		account *users.User
		code    string
	}{
		{"staff", true, &users.User{Login: "org", Email: "ana@example.edu", Roles: []string{"organizer"}}, "external_signin_staff_account"},
		{"blocked", true, &users.User{Login: "ana", Email: "ana@example.edu", Status: users.StatusBlocked, Roles: []string{users.RoleStudent}}, "account_blocked"},
		{"closed", false, nil, "external_signin_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMicrosoftFixture(t, microsoftOptions{createAccounts: tc.create})
			if tc.account != nil {
				f.repo.Add(*tc.account)
			}
			s := f.start(t, "192.0.2.1", "/")

			rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

			assertLandsOnSignIn(t, rec, tc.code)
			for _, c := range rec.Result().Cookies() {
				if c.Name == auth.NewCookieWriter(false).SessionName() && c.Value != "" {
					t.Errorf("a session cookie was set: %+v", c)
				}
			}
		})
	}
}

func TestAnOffSiteReturnPathLandsAtTheRoot(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "//evil.example/steal")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Errorf("callback = %d to %q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTheCallbackSpendsTheAddressBudgetBeforeTheExchange(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")

	// Refused callbacks spend the budget as well.
	for i := 0; i < api.MicrosoftSignInsPerAddress; i++ {
		f.callback("192.0.2.9", "code=x&state=y")
	}
	rec := f.signIn(t, "192.0.2.9", s, f.claims(msHomeTenant))

	assertLandsOnSignIn(t, rec, "external_signin_failed")
	if n := f.provider.Exchanges(); n != 0 {
		t.Errorf("the code was exchanged %d times past the budget, want none", n)
	}

	// Another address keeps its own.
	if rec := f.signIn(t, "198.51.100.1", f.start(t, "198.51.100.1", "/"), f.claims(msHomeTenant)); rec.Header().Get("Location") != "/" {
		t.Errorf("another address: callback to %q, want a sign-in", rec.Header().Get("Location"))
	}
}

func TestTheStartSpendsTheAddressBudget(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{})
	for i := 0; i < api.MicrosoftSignInsPerAddress; i++ {
		f.get("192.0.2.9", "/auth/microsoft/start")
	}

	rec := f.get("192.0.2.9", "/auth/microsoft/start")

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=external_signin_failed" {
		t.Errorf("start past the budget = %d to %q, want 302 to the sign-in page", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == msStateCookieName && c.Value != "" {
			t.Error("a refused start set a state cookie")
		}
	}
}

func TestMicrosoftRoutesDoNotExistWithoutAClient(t *testing.T) {
	f := newMicrosoftFixture(t, microsoftOptions{noClient: true})

	for _, path := range []string{"/auth/microsoft/start", "/auth/microsoft/callback?code=x&state=y"} {
		if rec := f.get("192.0.2.1", path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d without a client, want 404", path, rec.Code)
		}
	}
}
