package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
)

type handlerFixture struct {
	router http.Handler
	repo   *userstest.Repository
	user   users.User
}

func newHandlerFixture(t *testing.T) *handlerFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("student")
	hash, err := password.Hash(testPassword)
	if err != nil {
		t.Fatalf("password.Hash() returned error: %v", err)
	}
	user := repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", PasswordHash: hash,
		Status: users.StatusActive, Roles: []string{"student"},
	})

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	service := auth.NewService(auth.ServiceConfig{
		Users: repo, Sessions: sessions,
		Audit: audit.New(&apiSink{}), Limiter: auth.NewLimiter(c), Logger: log,
	})
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewAuthHandler(service, users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}), repo, mw, auth.NewCookieWriter(false), log).Mount(router)

	return &handlerFixture{router: router, repo: repo, user: user}
}

// post sends a JSON body to the router.
func (f *handlerFixture) post(path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// login performs a successful sign-in and returns the session cookie.
func (f *handlerFixture) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatal("login set no session cookie")
	return nil
}

func TestLoginEndpointSetsTheSessionCookie(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("no session cookie was set")
	}
}

func TestLoginResponseNeverCarriesTheSessionToken(t *testing.T) {
	// The token belongs in an HttpOnly cookie. Echoing it in the body would
	// hand it to any script on the page and undo that protection.
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	cookie := rec.Result().Cookies()[0]
	if strings.Contains(rec.Body.String(), cookie.Value) {
		t.Errorf("the response body contains the session token: %s", rec.Body.String())
	}
}

func TestLoginResponseNeverCarriesThePasswordHash(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	if strings.Contains(rec.Body.String(), "argon2id") || strings.Contains(rec.Body.String(), "password_hash") {
		t.Errorf("the response leaks the stored digest: %s", rec.Body.String())
	}
}

func TestLoginRejectsWrongCredentialsWith401(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"wrong"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLoginRejectsAMalformedBody(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestLoginRejectsAnOversizedBody(t *testing.T) {
	// The endpoint is unauthenticated; an unbounded body is free memory for
	// anyone who can reach it.
	f := newHandlerFixture(t)
	huge := `{"login":"` + strings.Repeat("a", 2*1024*1024) + `","password":"x"}`

	rec := f.post("/auth/login", huge)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want the oversized body to be refused", rec.Code)
	}
}

func TestLoginReportsThrottlingWith429(t *testing.T) {
	f := newHandlerFixture(t)
	var rec *httptest.ResponseRecorder
	for range maxLoginAttempts + 1 {
		rec = f.post("/auth/login", `{"login":"ivanov","password":"wrong"}`)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 after repeated failures", rec.Code)
	}
}

func TestLoginTellsTheClientAPasswordChangeIsDue(t *testing.T) {
	f := newHandlerFixture(t)
	hash, _ := password.Hash(testPassword)
	_ = f.repo.SetPassword(context.Background(), f.user.ID, hash, true)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	var body struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if !body.MustChangePassword {
		t.Error("the response does not tell the client to change the password")
	}
}

func TestCurrentUserEndpointDescribesTheSignedInAccount(t *testing.T) {
	f := newHandlerFixture(t)
	cookie := f.login(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Login       string   `json:"login"`
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Login != "ivanov" {
		t.Errorf("login = %q, want ivanov", body.Login)
	}
}

func TestCurrentUserEndpointNamesTheAccountAsAPersonWouldReadIt(t *testing.T) {
	// /auth/me was built for routing: a login and a permission list is all a
	// guard needs. A profile screen has to greet somebody, and the identity the
	// middleware assembles carries no name — deriving initials from a login is
	// how "i.ivanov" becomes "II" instead of "Ivan Ivanov" becoming "IV".
	f := newHandlerFixture(t)
	cookie := f.login(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	var body struct {
		FullName string   `json:"full_name"`
		Roles    []string `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.FullName != "Ivan Ivanov" {
		t.Errorf("full_name = %q, want Ivan Ivanov", body.FullName)
	}
	// The roles too: a profile says what somebody is, which permissions spell
	// out but do not name.
	if len(body.Roles) != 1 || body.Roles[0] != "student" {
		t.Errorf("roles = %v, want [student]", body.Roles)
	}
}

func TestCurrentUserEndpointRequiresASession(t *testing.T) {
	f := newHandlerFixture(t)
	rec := httptest.NewRecorder()

	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/me", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLogoutClearsTheCookieAndEndsTheSession(t *testing.T) {
	f := newHandlerFixture(t)
	cookie := f.login(t)

	rec := f.post("/auth/logout", "", cookie)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the session cookie was not cleared")
	}

	// The session must also be gone server-side, not merely forgotten by the
	// browser: a copied cookie has to stop working.
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(cookie)
	after := httptest.NewRecorder()
	f.router.ServeHTTP(after, req)
	if after.Code != http.StatusUnauthorized {
		t.Errorf("the session still works after logout: status = %d", after.Code)
	}
}

func TestPasswordChangeEndpointReplacesTheDigest(t *testing.T) {
	f := newHandlerFixture(t)
	cookie := f.login(t)

	rec := f.post("/auth/password",
		`{"old_password":"`+testPassword+`","new_password":"a brand new password"}`, cookie)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(f.user.ID)
	if ok, _ := password.Verify(stored.PasswordHash, "a brand new password"); !ok {
		t.Error("the new password does not verify against the stored digest")
	}
}

func TestPasswordChangeRejectsAWrongCurrentPassword(t *testing.T) {
	f := newHandlerFixture(t)
	cookie := f.login(t)

	rec := f.post("/auth/password", `{"old_password":"nope","new_password":"a brand new password"}`, cookie)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want the change to be refused", rec.Code)
	}
}

func TestPasswordChangeRequiresASession(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/password", `{"old_password":"x","new_password":"y"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestPasswordChangeEndsTheOtherSessions(t *testing.T) {
	// Changing a password is what someone does when they fear the account is
	// in use elsewhere; the session that made the change may survive, the rest
	// must not.
	f := newHandlerFixture(t)
	first := f.login(t)
	second := f.login(t)

	rec := f.post("/auth/password",
		`{"old_password":"`+testPassword+`","new_password":"a brand new password"}`, first)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password change failed: %s", rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(second)
	after := httptest.NewRecorder()
	f.router.ServeHTTP(after, req)

	if after.Code != http.StatusUnauthorized {
		t.Errorf("the other session survived the password change: status = %d", after.Code)
	}
}
