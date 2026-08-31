package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// staticRoles answers every contest-role lookup with one value.
type staticRoles struct{ role rbac.ContestRole }

func (s staticRoles) ContestRole(context.Context, uuid.UUID, uuid.UUID) (rbac.ContestRole, error) {
	return s.role, nil
}

type mwFixture struct {
	mw       *Middleware
	repo     *userstest.Repository
	sessions *SessionStore
	user     users.User
	token    string
}

func newMiddlewareFixture(t *testing.T, roles rbac.ContestRoleLoader, permissions ...string) *mwFixture {
	t.Helper()

	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("tester", permissions...)
	user := repo.Add(users.User{Login: "ivanov", Status: users.StatusActive, Roles: []string{"tester"}})

	sessions := NewSessionStore(c, time.Hour)
	token, err := sessions.Create(context.Background(), Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	mw := NewMiddleware(MiddlewareConfig{
		Sessions:   sessions,
		Users:      repo,
		Authorizer: rbac.New(roles),
		Cookies:    NewCookieWriter(false),
		Logger:     logging.New("error", io.Discard),
	})

	return &mwFixture{mw: mw, repo: repo, sessions: sessions, user: user, token: token}
}

// authed builds a request carrying the session cookie.
func authed(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return req
}

func okHandler(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

func TestAuthenticateRejectsARequestWithoutACookie(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthenticateRejectsAnUnknownToken(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed("not-a-session"))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthenticatePutsTheIdentityInContext(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{}, rbac.PermissionUsersManage)
	var seen rbac.Identity
	handler := f.mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = IdentityFrom(r.Context())
	}))

	handler.ServeHTTP(httptest.NewRecorder(), authed(f.token))

	if seen.UserID != f.user.ID {
		t.Errorf("UserID = %v, want %v", seen.UserID, f.user.ID)
	}
	if !seen.Has(rbac.PermissionUsersManage) {
		t.Error("the identity carries none of the account's permissions")
	}
}

func TestAuthenticateRejectsABlockedAccountHoldingAValidSession(t *testing.T) {
	// Blocking has to take effect on the next request, not when the session
	// happens to expire.
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), f.user.ID, users.StatusBlocked)
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a blocked account", rec.Code)
	}
}

func TestBlockedAccountLosesTheSessionEntirely(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), f.user.ID, users.StatusBlocked)

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), authed(f.token))

	if _, err := f.sessions.Get(context.Background(), f.token); err != ErrSessionNotFound {
		t.Error("the session of a blocked account was left in the store")
	}
}

func TestAuthenticateRejectsASessionFromAnEarlierGeneration(t *testing.T) {
	// This is how "log out everywhere" reaches sessions on other devices.
	f := newMiddlewareFixture(t, staticRoles{})
	if _, err := f.repo.BumpSessionGeneration(context.Background(), f.user.ID); err != nil {
		t.Fatalf("BumpSessionGeneration() returned error: %v", err)
	}
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a retired session", rec.Code)
	}
}

func TestAuthenticateAnswersWithAStructuredError(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (raw: %s)", err, rec.Body.String())
	}
	if body.Error.Code != "unauthenticated" {
		t.Errorf("error.code = %q, want unauthenticated", body.Error.Code)
	}
}

func TestRequirePermissionAllowsAHolder(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{}, rbac.PermissionUsersManage)
	rec := httptest.NewRecorder()
	handler := f.mw.Authenticate(f.mw.RequirePermission(rbac.PermissionUsersManage)(http.HandlerFunc(okHandler)))

	handler.ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestRequirePermissionDeniesWithoutIt(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{}, rbac.PermissionReportsView)
	rec := httptest.NewRecorder()
	handler := f.mw.Authenticate(f.mw.RequirePermission(rbac.PermissionUsersManage)(http.HandlerFunc(okHandler)))

	handler.ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestRequirePermissionWithoutAuthenticationIsUnauthenticatedNotForbidden(t *testing.T) {
	// 403 would tell an anonymous caller that the endpoint exists and that
	// they merely lack rights; 401 is both truthful and quieter.
	f := newMiddlewareFixture(t, staticRoles{})
	rec := httptest.NewRecorder()

	f.mw.RequirePermission(rbac.PermissionUsersManage)(http.HandlerFunc(okHandler)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// contestRouter mounts a handler under a contest id so the scoped middleware
// can read the parameter the way it will in production.
func contestRouter(f *mwFixture, permission string) http.Handler {
	r := chi.NewRouter()
	r.Route("/contests/{contestID}", func(r chi.Router) {
		r.Use(f.mw.Authenticate, f.mw.RequireContestPermission(permission))
		r.Get("/", okHandler)
	})
	return r
}

func contestRequest(token, contestID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/contests/"+contestID+"/", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	return req
}

func TestContestPermissionAllowsAManagerOfThatContest(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{role: rbac.RoleManager})
	rec := httptest.NewRecorder()

	contestRouter(f, rbac.PermissionContestEdit).ServeHTTP(rec, contestRequest(f.token, uuid.NewString()))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestContestPermissionDeniesSomeoneWithNoRoleThere(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{role: rbac.RoleNone}, rbac.PermissionContestEdit)
	rec := httptest.NewRecorder()

	contestRouter(f, rbac.PermissionContestEdit).ServeHTTP(rec, contestRequest(f.token, uuid.NewString()))

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 even though the global permission is held", rec.Code)
	}
}

func TestContestPermissionRejectsAMalformedContestID(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{role: rbac.RoleManager})
	rec := httptest.NewRecorder()

	contestRouter(f, rbac.PermissionContestEdit).ServeHTTP(rec, contestRequest(f.token, "not-a-uuid"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSystemAdministratorPassesTheContestScope(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{role: rbac.RoleNone}, rbac.PermissionContestAdminAll)
	rec := httptest.NewRecorder()

	contestRouter(f, rbac.PermissionContestEdit).ServeHTTP(rec, contestRequest(f.token, uuid.NewString()))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a system administrator", rec.Code)
	}
}

func TestAuthenticatedRequestCarriesTheUserIntoTheLogContext(t *testing.T) {
	// Every log line from an authenticated request should be attributable.
	f := newMiddlewareFixture(t, staticRoles{})
	var seen string
	handler := f.mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = logging.UserIDFrom(r.Context())
	}))

	handler.ServeHTTP(httptest.NewRecorder(), authed(f.token))

	if seen != f.user.ID.String() {
		t.Errorf("log context user_id = %q, want %q", seen, f.user.ID)
	}
}

func TestActiveSessionIsExtendedOnUse(t *testing.T) {
	// Sliding expiry: someone working through a contest must not be logged out
	// mid-answer.
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	user := repo.Add(users.User{Login: "ivanov", Status: users.StatusActive})
	sessions := NewSessionStore(c, 80*time.Millisecond)
	token, _ := sessions.Create(context.Background(), Principal{UserID: user.ID, Login: user.Login})
	mw := NewMiddleware(MiddlewareConfig{
		Sessions:   sessions,
		Users:      repo,
		Authorizer: rbac.New(staticRoles{}),
		Cookies:    NewCookieWriter(false),
		Logger:     logging.New("error", io.Discard),
	})

	time.Sleep(50 * time.Millisecond)
	mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), authed(token))
	time.Sleep(50 * time.Millisecond)

	if _, err := sessions.Get(context.Background(), token); err != nil {
		t.Errorf("the session expired although it was used: %v", err)
	}
}

func TestOneTimePasswordAccountIsBlockedFromTheAPI(t *testing.T) {
	// must_change_password was advisory: a client ignoring the UI kept the
	// administrator-issued password as a working credential indefinitely.
	f := newMiddlewareFixture(t, staticRoles{})
	setMustChange(t, f)
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 while the password change is pending", rec.Code)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Error.Code != "password_change_required" {
		t.Errorf("error.code = %q, want password_change_required so the client can route to the form", body.Error.Code)
	}
}

func TestOneTimePasswordAccountMayStillChangeItsPassword(t *testing.T) {
	// The enforcement must not wall off the only way out.
	f := newMiddlewareFixture(t, staticRoles{})
	setMustChange(t, f)

	for _, path := range []string{"/api/v1/auth/password", "/api/v1/auth/logout", "/api/v1/auth/me"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.token})
		rec := httptest.NewRecorder()

		f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 while the password change is pending", path, rec.Code)
		}
	}
}

func TestTheWayOutIsNamedExactly(t *testing.T) {
	// The exemption was a suffix match, so any route whose path happened to
	// end in one of the three was exempt too. Nothing does today; the point is
	// that adding /contests/{id}/auth/me tomorrow would silently reopen the
	// API to an account still carrying somebody else's handover password, and
	// nothing in that change would look like a security decision.
	f := newMiddlewareFixture(t, staticRoles{})
	setMustChange(t, f)

	for _, path := range []string{
		"/api/v1/contests/c-1/auth/me",
		"/api/v1/auth/password/reset",
		"/hack/auth/logout",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.token})
		rec := httptest.NewRecorder()

		f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403: only the three named endpoints are a way out", path, rec.Code)
		}
	}
}

func TestTheWayOutWorksWhereverTheAPIIsMounted(t *testing.T) {
	// Production mounts under /api/v1 and the tests mount bare. Both have to
	// reach the same three endpoints, which is what the suffix match bought
	// and what the exact match must not lose.
	f := newMiddlewareFixture(t, staticRoles{})
	setMustChange(t, f)

	for _, path := range []string{"/auth/password", "/api/v1/auth/password"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.token})
		rec := httptest.NewRecorder()

		f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}

// setMustChange flags the fixture account as still on its one-time password.
func setMustChange(t *testing.T, f *mwFixture) {
	t.Helper()
	if err := f.repo.SetPassword(context.Background(), f.user.ID, "$argon2id$stub", true); err != nil {
		t.Fatalf("SetPassword returned error: %v", err)
	}
}
