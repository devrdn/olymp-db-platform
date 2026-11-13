package api_test

import (
	"context"
	"errors"
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
	"github.com/devrdn/db-contest/backend/internal/connector"
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

var msStateCookieName = auth.NewCookieWriter(false).StateName(users.ProviderMicrosoft)

type externalFixture struct {
	provider *microsofttest.Provider
	repo     *userstest.Repository
	sessions *auth.SessionStore
	auth     *auth.Service
	logs     *logBuffer
	trail    *loginTrail
	router   http.Handler
}

type externalOptions struct {
	createAccounts bool
	noClient       bool
	extra          []api.SignInProvider
}

func newExternalFixture(t *testing.T, opts externalOptions) *externalFixture {
	t.Helper()
	p := microsofttest.NewProvider(t)
	repo := userstest.New()
	repo.GrantRole(users.RoleStudent)
	repo.GrantRole("organizer", "contests.manage")

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })
	logs := &logBuffer{}
	log := logging.New("warn", logs)
	sessions := auth.NewSessionStore(c, time.Hour)
	limiter := auth.NewLimiter(c)
	trail := &loginTrail{}
	authService := auth.NewService(auth.ServiceConfig{
		Users: repo, Sessions: sessions,
		Audit: audit.New(trail), Limiter: limiter, Logger: log,
		Passwords: passwordtest.NewHasher(),
		Devices:   devices(t),
	})
	cookies := auth.NewCookieWriter(false)
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: cookies, Logger: log,
	})
	service := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()).
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

	providers := []api.SignInProvider{{
		ID: users.ProviderMicrosoft, States: externalStates(users.ProviderMicrosoft),
		Variables: []string{"MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"},
	}}
	if client != nil {
		providers[0].Connector = client
	}
	providers = append(providers, opts.extra...)

	router := chi.NewRouter()
	api.NewExternalSignInHandler(api.ExternalSignIn{
		Providers: providers,
		Accounts: federation.NewService(federation.Config{
			Policy: federation.Policy{CreateAccounts: opts.createAccounts},
			Store:  repo, Accounts: service, PendingLogins: noPendingLogins{},
		}),
		Auth: authService, Limiter: limiter, Middleware: mw, Cookies: cookies, Log: log,
	}).Mount(router)
	return &externalFixture{provider: p, repo: repo, sessions: sessions, auth: authService, logs: logs, trail: trail, router: router}
}

func externalStates(provider string) *connector.StateSealer {
	return connector.NewStateSealer([]byte(strings.Repeat("k", 32)), provider, nil)
}

// stubConnector is a provider that sends the browser to a fixed address and
// answers every exchange with the claims it holds.
type stubConnector struct {
	authorize string
	claims    federation.Claims
	exchanges int
}

func (s *stubConnector) AuthCodeURL(st connector.State) string {
	return s.authorize + "?state=" + st.State
}

func (s *stubConnector) Exchange(context.Context, string, connector.State) (federation.Claims, error) {
	s.exchanges++
	return s.claims, nil
}

func stubProvider(id string, c *stubConnector) api.SignInProvider {
	return api.SignInProvider{ID: id, Connector: c, States: externalStates(id)}
}

// loginTrail keeps the audit entries of the sign-ins it sees.
type loginTrail struct{ entries []audit.Entry }

func (l *loginTrail) Append(_ context.Context, e audit.Entry) error {
	l.entries = append(l.entries, e)
	return nil
}

func (l *loginTrail) AppendMany(_ context.Context, entries []audit.Entry) error {
	l.entries = append(l.entries, entries...)
	return nil
}

func (l *loginTrail) loginMethod() any {
	for _, e := range l.entries {
		if e.Action == "auth.login" {
			return e.Payload["method"]
		}
	}
	return nil
}

type noPendingLogins struct{}

func (noPendingLogins) LoginHeld(context.Context, string, string, time.Time) (bool, error) {
	return false, nil
}

func (f *externalFixture) get(from, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
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

func (f *externalFixture) start(t *testing.T, from, returnPath string) started {
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
func (f *externalFixture) signIn(t *testing.T, from string, s started, claims map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	claims["nonce"] = s.query.Get("nonce")
	f.provider.Issue("code-"+s.query.Get("state"), f.provider.Sign(t, claims), s.query.Get("code_challenge"))
	return f.callback(from, "code=code-"+s.query.Get("state")+"&state="+s.query.Get("state"), s.cookie)
}

func (f *externalFixture) callback(from, query string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return f.get(from, "/auth/microsoft/callback?"+query, cookies...)
}

func (f *externalFixture) claims(tid string) map[string]any {
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
	assertStateNamedCleared(t, rec, msStateCookieName)
}

func assertStateNamedCleared(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
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
	f := newExternalFixture(t, externalOptions{})

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
	f := newExternalFixture(t, externalOptions{createAccounts: true})
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
	f := newExternalFixture(t, externalOptions{createAccounts: true})
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

func TestAHomeTenantMemberSignsIntoAStaffAccount(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	org := f.repo.Add(users.User{Login: "org", FullName: "Org", Email: "ana@example.edu", Roles: []string{"organizer"}})
	s := f.start(t, "192.0.2.1", "/contests/42")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/contests/42" {
		t.Fatalf("callback = %d to %q, want 302 to the return path (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	principal, err := f.sessions.Get(t.Context(), sessionOf(t, rec).Value)
	if err != nil || principal.UserID != org.ID {
		t.Errorf("session = %+v, %v; want the organizer's", principal, err)
	}
}

// A tenant that merely owns the address's domain is not the installation's
// directory, so it never enters staff.
func TestAForeignTenantNeverEntersAStaffAccount(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	f.repo.Add(users.User{Login: "org", FullName: "Org", Email: "ana@example.edu", Roles: []string{"organizer"}})
	s := f.start(t, "192.0.2.1", "/")
	claims := f.claims(msOtherTenant)
	claims["xms_edov"] = true

	rec := f.signIn(t, "192.0.2.1", s, claims)

	assertLandsOnSignIn(t, rec, "external_signin_staff_account")
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.NewCookieWriter(false).SessionName() && c.Value != "" {
			t.Errorf("a session cookie was set: %+v", c)
		}
	}
}

func TestTheCallbackRefusesAStateItDidNotIssue(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")
	other := connector.NewStateSealer([]byte(strings.Repeat("o", 32)), users.ProviderMicrosoft, nil)
	forged := &http.Cookie{Name: msStateCookieName, Value: other.Seal(connector.State{
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

// A code Microsoft issued to another sign-in (its PKCE challenge) cannot be
// redeemed from this browser, even with this browser's nonce in the token.
func TestACodeIssuedToAnotherSignInCannotBeRedeemed(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	victim := f.start(t, "192.0.2.1", "/")
	attacker := f.start(t, "192.0.2.1", "/")
	claims := f.claims(msHomeTenant)
	claims["nonce"] = attacker.query.Get("nonce")
	f.provider.Issue("stolen", f.provider.Sign(t, claims), victim.query.Get("code_challenge"))

	rec := f.callback("192.0.2.1", "code=stolen&state="+attacker.query.Get("state"), attacker.cookie)

	assertLandsOnSignIn(t, rec, "external_signin_failed")
	if f.provider.Exchanges() != 1 || f.repo.Len() != 0 {
		t.Errorf("exchanges %d, accounts %d; want the one refused exchange and no account", f.provider.Exchanges(), f.repo.Len())
	}
}

// An outage of Microsoft's keys is the installation's to hear about, not a
// forged token.
func TestAKeyOutageIsLoggedAsAnError(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	f.provider.FailKeys(http.StatusServiceUnavailable)
	s := f.start(t, "192.0.2.1", "/")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	assertLandsOnSignIn(t, rec, "external_signin_failed")
	if !f.logs.loggedError("signing keys could not be fetched") {
		t.Error("the key outage was not logged as an error")
	}
}

// An account linked to another identity at the same issuer will be refused
// on every try, so the page must not say to try again.
func TestASecondIdentityForALinkedAccountLandsOnTheConflictCode(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	ana := f.repo.Add(users.User{Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent}})
	if err := f.repo.LinkExternalIdentity(t.Context(), users.ExternalIdentity{
		UserID: ana.ID, Provider: users.ProviderMicrosoft, Issuer: f.provider.Issuer(msHomeTenant), Subject: "an-earlier-object-id",
	}); err != nil {
		t.Fatal(err)
	}
	s := f.start(t, "192.0.2.1", "/")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	assertLandsOnSignIn(t, rec, "external_signin_conflict")
}

func TestARefusedTokenLandsOnTheSignInPage(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")
	claims := f.claims(msHomeTenant)
	claims["aud"] = "66666666-7777-4888-9999-aaaaaaaaaaaa"

	rec := f.signIn(t, "192.0.2.1", s, claims)

	assertLandsOnSignIn(t, rec, "external_signin_failed")
	if f.repo.Len() != 0 {
		t.Errorf("%d accounts exist after a refused token, want none", f.repo.Len())
	}
	if f.logs.loggedError("") {
		t.Error("a refused token was logged as an error, as an outage would be")
	}
}

func TestARefusedAccountLandsOnItsCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		create  bool
		account *users.User
		code    string
	}{
		{"blocked", true, &users.User{Login: "ana", Email: "ana@example.edu", Status: users.StatusBlocked, Roles: []string{users.RoleStudent}}, "account_blocked"},
		{"closed", false, nil, "external_signin_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newExternalFixture(t, externalOptions{createAccounts: tc.create})
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

// The sign-in page sends a signed-in visitor where a password sign-in would,
// so the destination rule lives in one place.
func TestAnOffSiteOrMissingReturnPathLandsOnTheSignInPage(t *testing.T) {
	for name, returnPath := range map[string]string{"off-site": "//evil.example/steal", "missing": ""} {
		t.Run(name, func(t *testing.T) {
			f := newExternalFixture(t, externalOptions{createAccounts: true})
			s := f.start(t, "192.0.2.1", returnPath)

			rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

			if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
				t.Errorf("callback = %d to %q, want 302 to /login", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

// A roster-made student signs in with Microsoft because they never had the
// one-time password; it must not stand between them and every screen.
func TestASignInToAnAccountOnItsOneTimePasswordDropsIt(t *testing.T) {
	const issued = "issued one-time password"
	f := newExternalFixture(t, externalOptions{})
	ana := f.repo.Add(users.User{
		Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent},
		PasswordHash: passwordtest.Hash(t, issued), MustChangePassword: true,
	})
	s := f.start(t, "192.0.2.1", "/my")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/my" {
		t.Fatalf("callback = %d to %q, want 302 to the return path (%s)", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	principal, err := f.sessions.Get(t.Context(), sessionOf(t, rec).Value)
	if err != nil || principal.UserID != ana.ID {
		t.Fatalf("session %+v, %v; want ana's", principal, err)
	}
	stored, _ := f.repo.Get(ana.ID)
	if stored.HasPassword() || stored.MustChangePassword {
		t.Errorf("stored hash %q, must change %v: want no password and no forced change", stored.PasswordHash, stored.MustChangePassword)
	}
	if principal.Generation != stored.SessionGeneration {
		t.Errorf("session at generation %d, account at %d: the new session must outlive the retirement", principal.Generation, stored.SessionGeneration)
	}
	if _, err := f.auth.Login(t.Context(), auth.LoginCommand{Login: "ana", Password: issued, IP: "192.0.2.1"}); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a password sign-in with the dropped password = %v, want ErrInvalidCredentials", err)
	}
}

// A one-time password issued after the link was handed to the person on
// purpose: the sign-in keeps it and sends them to change it.
func TestAnAccountThatMustChangeItsPasswordLandsOnThePasswordScreen(t *testing.T) {
	f := newExternalFixture(t, externalOptions{})
	ana := f.repo.Add(users.User{
		Login: "ana", FullName: "Ana", Email: "ana@example.edu", Roles: []string{users.RoleStudent},
		PasswordHash: passwordtest.Hash(t, "issued again"), MustChangePassword: true,
	})
	if err := f.repo.LinkExternalIdentity(t.Context(), users.ExternalIdentity{
		UserID: ana.ID, Provider: users.ProviderMicrosoft, Issuer: f.provider.Issuer(msHomeTenant), Subject: msObjectID,
	}); err != nil {
		t.Fatal(err)
	}
	s := f.start(t, "192.0.2.1", "/my")

	rec := f.signIn(t, "192.0.2.1", s, f.claims(msHomeTenant))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/password" {
		t.Errorf("callback = %d to %q, want 302 to /password", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTheCallbackSpendsTheAddressBudgetBeforeTheExchange(t *testing.T) {
	f := newExternalFixture(t, externalOptions{createAccounts: true})
	s := f.start(t, "192.0.2.1", "/")

	// Refused callbacks spend the budget as well.
	for i := 0; i < api.ExternalSignInsPerAddress; i++ {
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
	f := newExternalFixture(t, externalOptions{})
	for i := 0; i < api.ExternalSignInsPerAddress; i++ {
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
	f := newExternalFixture(t, externalOptions{noClient: true})

	for _, path := range []string{"/auth/microsoft/start", "/auth/microsoft/callback?code=x&state=y"} {
		if rec := f.get("192.0.2.1", path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d without a client, want 404", path, rec.Code)
		}
	}
}

func TestMicrosoftKeepsItsRoutes(t *testing.T) {
	f := newExternalFixture(t, externalOptions{})

	start := f.get("192.0.2.1", "/auth/microsoft/start")
	if location := start.Header().Get("Location"); start.Code != http.StatusFound || !strings.HasPrefix(location, f.provider.URL+"/common/oauth2/v2.0/authorize") {
		t.Errorf("start = %d to %q, want 302 to the fake's authorize endpoint", start.Code, location)
	}
	if rec := f.get("192.0.2.1", "/auth/microsoft/callback"); rec.Code == http.StatusNotFound {
		t.Error("/auth/microsoft/callback is not mounted")
	}
}

func TestEachConfiguredProviderHasItsOwnRoutes(t *testing.T) {
	stub := &stubConnector{authorize: "https://accounts.example.test/authorize"}
	f := newExternalFixture(t, externalOptions{extra: []api.SignInProvider{
		stubProvider(users.ProviderGoogle, stub),
		{ID: users.ProviderGitHub, States: externalStates(users.ProviderGitHub)},
	}})

	rec := f.get("192.0.2.1", "/auth/google/start")
	if location := rec.Header().Get("Location"); rec.Code != http.StatusFound || !strings.HasPrefix(location, stub.authorize) {
		t.Errorf("google start = %d to %q, want 302 to the stub", rec.Code, location)
	}
	cookieNamed(t, rec, "dbcontest_google_state")
	for _, path := range []string{"/auth/github/start", "/auth/github/callback"} {
		if rec := f.get("192.0.2.1", path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d without a connector, want 404", path, rec.Code)
		}
	}
}

func TestTheCallbackSignsInAsTheProvidersMethod(t *testing.T) {
	stub := &stubConnector{authorize: "https://accounts.example.test/authorize", claims: federation.Claims{
		Provider: users.ProviderGoogle, Issuer: "https://accounts.google.com", Subject: "1234567890",
		Email: "ana@example.edu", EmailTrusted: true, Name: "Ana",
	}}
	f := newExternalFixture(t, externalOptions{createAccounts: true, extra: []api.SignInProvider{stubProvider(users.ProviderGoogle, stub)}})

	start := f.get("192.0.2.1", "/auth/google/start")
	location, _ := url.Parse(start.Header().Get("Location"))
	rec := f.get("192.0.2.1", "/auth/google/callback?code=c&state="+location.Query().Get("state"), cookieNamed(t, start, "dbcontest_google_state"))

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
		t.Fatalf("callback = %d to %q, want 302 to the default return path", rec.Code, rec.Header().Get("Location"))
	}
	sessionOf(t, rec)
	if got := f.trail.loginMethod(); got != users.ProviderGoogle {
		t.Errorf("auth.login method = %v, want %q", got, users.ProviderGoogle)
	}
}

func TestAStateSealedForOneProviderIsRefusedByAnother(t *testing.T) {
	stub := &stubConnector{authorize: "https://accounts.example.test/authorize"}
	f := newExternalFixture(t, externalOptions{createAccounts: true, extra: []api.SignInProvider{stubProvider(users.ProviderGoogle, stub)}})
	s := f.start(t, "192.0.2.1", "/")
	borrowed := &http.Cookie{Name: "dbcontest_google_state", Value: s.cookie.Value}

	rec := f.get("192.0.2.1", "/auth/google/callback?code=c&state="+s.query.Get("state"), borrowed)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?error=external_signin_failed" {
		t.Errorf("answered %d to %q, want 302 to /login?error=external_signin_failed", rec.Code, rec.Header().Get("Location"))
	}
	assertStateNamedCleared(t, rec, "dbcontest_google_state")
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionName {
			t.Error("a session was opened")
		}
	}
	if stub.exchanges != 0 {
		t.Errorf("Exchange called %d times, want none", stub.exchanges)
	}
}
