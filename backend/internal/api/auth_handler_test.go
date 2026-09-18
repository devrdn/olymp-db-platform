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
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type handlerFixture struct {
	router http.Handler
	repo   *userstest.Repository
	user   users.User
	// hasher has a single slot and a short wait, so a test can fill it and
	// see the refusal an overloaded process answers with.
	hasher *password.Hasher
}

func newHandlerFixture(t *testing.T) *handlerFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("student")
	hash := passwordtest.Hash(t, testPassword)
	user := repo.Add(users.User{
		Login: "ivanov", FullName: "Ivan Ivanov", PasswordHash: hash,
		Status: users.StatusActive, Roles: []string{"student"},
	})

	log := logging.New("error", io.Discard)
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 50 * time.Millisecond})
	// The maximum lifetime below the idle timeout, so the cookie's lifetime
	// shows which of the two it was set from.
	sessions := auth.NewSessionStore(c, time.Hour).WithMaxLifetime(30 * time.Minute)
	service := auth.NewService(auth.ServiceConfig{
		Users: repo, Sessions: sessions,
		Audit: audit.New(&apiSink{}), Limiter: auth.NewLimiter(c), Logger: log,
		Passwords: hasher,
		Devices:   devices(t),
	})
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewAuthHandler(service, users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, hasher), repo, mw, auth.NewCookieWriter(false), log).Mount(router)

	return &handlerFixture{router: router, repo: repo, user: user, hasher: hasher}
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

// TestLoginResponseDoesNotCarryStatusChangeMetadata pins the fix for the
// leak toUserResponse's widening for the account card introduced: an
// unblocked account is active again and its reason is empty, but its
// StatusChangedAt/By/ByLogin still name the administrator and the moment of
// the last change. login used to share toUserResponse verbatim with the
// account-management screens, so that metadata — an administrator's UUID and
// login among it — rode along into the response an ordinary account owner
// gets merely by signing in.
func TestLoginResponseDoesNotCarryStatusChangeMetadata(t *testing.T) {
	f := newHandlerFixture(t)

	withHistory := f.user
	changedAt := time.Now().UTC()
	changedBy := uuid.New()
	withHistory.StatusChangedAt = &changedAt
	withHistory.StatusChangedBy = &changedBy
	withHistory.StatusChangedByLogin = "root"
	f.repo.Add(withHistory)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	for _, field := range []string{"status_changed_at", "status_changed_by", "status_changed_by_login"} {
		if strings.Contains(rec.Body.String(), field) {
			t.Errorf("the sign-in response names %q — administrator status-change metadata "+
				"that is not the account owner's business: %s", field, rec.Body.String())
		}
	}
}

func TestLoginRejectsWrongCredentialsWith401(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"wrong"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestLoginRejectsAnOverlongLoginTheSameWayAsAWrongOne(t *testing.T) {
	// A login longer than any real account can have (users.MaxLoginLength)
	// must be answered exactly like an ordinary wrong login — not a code of
	// its own, which would let a caller use the length bound to tell an
	// existing login from an impossible one.
	f := newHandlerFixture(t)
	overlong := strings.Repeat("a", users.MaxLoginLength+1)

	rec := f.post("/auth/login", `{"login":"`+overlong+`","password":"whatever"}`)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "invalid_credentials" {
		t.Errorf("error code = %q, want invalid_credentials", code)
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
	hash := passwordtest.Hash(t, testPassword)
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
	if !passwordtest.Matches(t, stored.PasswordHash, "a brand new password") {
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

func TestPasswordChangeReportsThrottlingWith429(t *testing.T) {
	// A borrowed session must not be a place to guess the current password;
	// the endpoint is throttled like the sign-in it resembles.
	f := newHandlerFixture(t)
	cookie := f.login(t)

	var rec *httptest.ResponseRecorder
	for range maxLoginAttempts + 1 {
		rec = f.post("/auth/password", `{"old_password":"nope","new_password":"a brand new password"}`, cookie)
	}

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 after repeated wrong current passwords", rec.Code)
	}
}

// holdEveryHashingSlot fills the fixture's hasher until the test ends.
func (f *handlerFixture) holdEveryHashingSlot(t *testing.T) {
	t.Helper()
	slot, err := f.hasher.Hold(context.Background())
	if err != nil {
		t.Fatalf("Hold() returned error: %v", err)
	}
	t.Cleanup(slot.Release)
}

func TestLoginReportsBusyHashingWith503(t *testing.T) {
	// Not 401: the password was never checked, and telling somebody who typed
	// it correctly that it was wrong sends them to reset a password that works.
	f := newHandlerFixture(t)
	f.holdEveryHashingSlot(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "sign_in_busy" {
		t.Errorf("code = %q, want sign_in_busy", code)
	}
}

func TestPasswordChangeReportsBusyHashingWith503(t *testing.T) {
	f := newHandlerFixture(t)
	cookie := f.login(t)
	f.holdEveryHashingSlot(t)

	rec := f.post("/auth/password", `{"old_password":"`+testPassword+`","new_password":"a brand new password"}`, cookie)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "sign_in_busy" {
		t.Errorf("code = %q, want sign_in_busy", code)
	}
}

func TestTheSessionCookieExpiresWithTheSessionsMaximumLifetime(t *testing.T) {
	f := newHandlerFixture(t)

	cookie := f.login(t)

	if cookie.MaxAge != int((30 * time.Minute).Seconds()) {
		t.Errorf("session cookie Max-Age = %d, want %d: the maximum lifetime is shorter than the idle timeout",
			cookie.MaxAge, int((30 * time.Minute).Seconds()))
	}
}

// devices is device trust for the handler tests, keyed by a fixed secret.
func devices(t *testing.T) *auth.DeviceTrust {
	t.Helper()
	trust, err := auth.NewDeviceTrust([]byte(strings.Repeat("k", auth.MinDeviceSecretLength)), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewDeviceTrust() returned error: %v", err)
	}
	return trust
}

func responseCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestLoginMarksTheBrowserAsOneTheOwnerSignedInFrom(t *testing.T) {
	f := newHandlerFixture(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)

	device := responseCookie(rec, auth.DeviceCookieName)
	if device == nil || device.Value == "" {
		t.Fatalf("no device cookie was set on a successful sign-in; cookies: %v", rec.Result().Cookies())
	}
	if !device.HttpOnly || device.MaxAge != int((30*24*time.Hour).Seconds()) {
		t.Errorf("device cookie = %+v, want HttpOnly and thirty days", device)
	}
	if failed := f.post("/auth/login", `{"login":"ivanov","password":"wrong"}`); responseCookie(failed, auth.DeviceCookieName) != nil {
		t.Error("a failed sign-in set a device cookie")
	}
}

func TestTheOwnersBrowserSignsInThroughALockoutAtItsOwnAddress(t *testing.T) {
	// Every request here comes from httptest's one address, as a lecture
	// hall's do: the rival spends the guessing limit, and the owner's browser,
	// holding the cookie from an earlier sign-in, still gets in.
	f := newHandlerFixture(t)
	first := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`)
	device := responseCookie(first, auth.DeviceCookieName)
	if device == nil {
		t.Fatal("no device cookie from the first sign-in")
	}

	for range maxLoginAttempts + 1 {
		f.post("/auth/login", `{"login":"ivanov","password":"a rival's guess"}`)
	}
	if rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("without the device cookie: status = %d, want 429", rec.Code)
	}

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`,
		&http.Cookie{Name: auth.DeviceCookieName, Value: device.Value})
	if rec.Code != http.StatusOK {
		t.Errorf("with the device cookie: status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Signing in again from a browser that is already signed in replaces its
// session: the cookie it came with stops working, and a cookie that names no
// session does not stand in the way of signing in.
func TestSigningInAgainEndsTheBrowsersPreviousSession(t *testing.T) {
	f := newHandlerFixture(t)
	first := f.login(t)

	rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`, first)
	if rec.Code != http.StatusOK {
		t.Fatalf("second login status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	second := responseCookie(rec, auth.SessionCookieName)
	if second == nil || second.Value == first.Value {
		t.Fatal("the second login issued no new session cookie")
	}

	me := func(cookie *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		f.router.ServeHTTP(out, req)
		return out.Code
	}
	if code := me(first); code != http.StatusUnauthorized {
		t.Errorf("the replaced session still authenticates: status = %d", code)
	}
	if code := me(second); code != http.StatusOK {
		t.Errorf("the new session does not authenticate: status = %d", code)
	}

	stale := &http.Cookie{Name: auth.SessionCookieName, Value: "garbled"}
	if rec := f.post("/auth/login", `{"login":"ivanov","password":"`+testPassword+`"}`, stale); rec.Code != http.StatusOK {
		t.Errorf("a login carrying a stale cookie = %d, want 200", rec.Code)
	}
}
