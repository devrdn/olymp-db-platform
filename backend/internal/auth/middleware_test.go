package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

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
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a blocked account", rec.Code)
	}
}

func TestBlockedAccountLosesTheSessionEntirely(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), authed(f.token))

	if _, err := f.sessions.Get(context.Background(), f.token); err != ErrSessionNotFound {
		t.Error("the session of a blocked account was left in the store")
	}
}

func TestAuthenticateRejectsADeletedAccountHoldingAValidSession(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusDeleted, users.StatusChange{})
	rec := httptest.NewRecorder()

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(f.token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a deleted account", rec.Code)
	}
}

func TestDeletedAccountLosesTheSessionEntirely(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusDeleted, users.StatusChange{})

	f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(httptest.NewRecorder(), authed(f.token))

	if _, err := f.sessions.Get(context.Background(), f.token); err != ErrSessionNotFound {
		t.Error("the session of a deleted account was left in the store")
	}
}

func TestAuthenticateRejectsASessionFromAnEarlierGeneration(t *testing.T) {
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
	f := newMiddlewareFixture(t, staticRoles{})
	rec := httptest.NewRecorder()

	f.mw.RequirePermission(rbac.PermissionUsersManage)(http.HandlerFunc(okHandler)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// contestRouter mounts a handler under {contestID} as production does.
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
	// A suffix match would exempt any route ending like an exempt one.
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
	// Bare, /api/v1 and /api/v2 must all reach the exempt paths.
	f := newMiddlewareFixture(t, staticRoles{})
	setMustChange(t, f)

	for _, path := range []string{"/auth/password", "/api/v1/auth/password", "/api/v2/auth/password"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: f.token})
		rec := httptest.NewRecorder()

		f.mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}

func setMustChange(t *testing.T, f *mwFixture) {
	t.Helper()
	if err := f.repo.SetPassword(context.Background(), f.user.ID, "$argon2id$stub", true); err != nil {
		t.Fatalf("SetPassword returned error: %v", err)
	}
}

func TestAuthenticateRefusesASessionPastItsMaximumLifetimeHoweverRecentlyUsed(t *testing.T) {
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	repo := userstest.New()
	user := repo.Add(users.User{Login: "ivanov", Status: users.StatusActive})
	sessions := NewSessionStore(c, time.Hour).WithMaxLifetime(12 * time.Hour)
	token, err := sessions.Create(context.Background(), Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	now := time.Now().UTC()
	record, _ := json.Marshal(Session{UserID: user.ID, Login: user.Login, IssuedAt: now.Add(-13 * time.Hour), RefreshedAt: now})
	_ = c.Set(context.Background(), sessionKey(token), record, time.Hour)

	mw := NewMiddleware(MiddlewareConfig{
		Sessions: sessions, Users: repo, Authorizer: rbac.New(staticRoles{}),
		Cookies: NewCookieWriter(false), Logger: logging.New("error", io.Discard),
	})
	rec := httptest.NewRecorder()
	mw.Authenticate(http.HandlerFunc(okHandler)).ServeHTTP(rec, authed(token))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a session past its maximum lifetime", rec.Code)
	}
}

type countingUsers struct {
	*userstest.Repository
	mu    sync.Mutex
	reads int
}

func (c *countingUsers) ByID(ctx context.Context, id uuid.UUID) (users.User, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.Repository.ByID(ctx, id)
}

func (c *countingUsers) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// cachedFixture assembles the middleware as the deployment does, with the
// account cache and the user service sharing one store.
type cachedFixture struct {
	mw       *Middleware
	repo     *userstest.Repository
	counted  *countingUsers
	sessions *SessionStore
	// store can fail without failing the session store.
	store   *switchableCache
	service *users.Service
	user    users.User
	token   string
}

const cachedFixturePassword = "the current password"

func newCachedFixture(t *testing.T, ttl time.Duration) *cachedFixture {
	t.Helper()

	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	log := logging.New("error", io.Discard)

	repo := userstest.New()
	repo.GrantRole("tester", rbac.PermissionUsersManage)
	user := repo.Add(users.User{
		Login: "ivanov", FullName: "Ivanov", Status: users.StatusActive, Roles: []string{"tester"},
		PasswordHash: passwordtest.Hash(t, cachedFixturePassword),
	})
	counted := &countingUsers{Repository: repo}

	sessions := NewSessionStore(c, time.Hour)
	token, err := sessions.Create(context.Background(), Principal{UserID: user.ID, Login: user.Login})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	store := newSwitchableCache(t)
	accounts := NewAccountCache(store, ttl, log)
	mw := NewMiddleware(MiddlewareConfig{
		Sessions:   sessions,
		Users:      counted,
		Accounts:   accounts,
		Authorizer: rbac.New(staticRoles{}),
		Cookies:    NewCookieWriter(false),
		Logger:     log,
	})
	service := users.NewService(repo, audit.New(&collectingSink{}), &userstest.SpyUnitOfWork{}, passwordtest.NewHasher()).
		WithAccessCache(accounts)

	return &cachedFixture{
		mw: mw, repo: repo, counted: counted, sessions: sessions, store: store,
		service: service, user: user, token: token,
	}
}

func (f *cachedFixture) serve(token string) (int, rbac.Identity) {
	var seen rbac.Identity
	handler := f.mw.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, authed(token))
	return rec.Code, seen
}

func TestACachedAccountSparesTheDatabaseReadAndTheCacheWrite(t *testing.T) {
	f := newCachedFixture(t, time.Minute)

	for i := range 5 {
		code, identity := f.serve(f.token)
		if code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, code)
		}
		if identity.UserID != f.user.ID || !identity.Has(rbac.PermissionUsersManage) {
			t.Fatalf("request %d: identity %+v lacks the account or its permissions", i, identity)
		}
	}

	if got := f.counted.readCount(); got != 1 {
		t.Errorf("five requests read the account %d times, want once", got)
	}
	if got := f.store.setCount(); got != 1 {
		t.Errorf("five requests wrote the account cache %d times, want once", got)
	}
}

// The TTL is far longer than the test, so only invalidation can pass it.
func TestEveryChangeToAccessIsHonouredOnTheNextRequest(t *testing.T) {
	actor := uuid.New()
	operations := []struct {
		name string
		run  func(f *cachedFixture) error
	}{
		{"Block", func(f *cachedFixture) error {
			return f.service.Block(context.Background(), actor, f.user.ID, "cheating")
		}},
		{"Delete", func(f *cachedFixture) error {
			return f.service.Delete(context.Background(), actor, f.user.ID, "graduated")
		}},
		{"BulkSetStatus", func(f *cachedFixture) error {
			_, err := f.service.BulkSetStatus(context.Background(), actor, []uuid.UUID{f.user.ID}, users.StatusBlocked, "cheating")
			return err
		}},
		{"ReplaceRoles", func(f *cachedFixture) error {
			return f.service.ReplaceRoles(context.Background(), actor, f.user.ID, nil)
		}},
		{"BulkReplaceRoles", func(f *cachedFixture) error {
			_, err := f.service.BulkReplaceRoles(context.Background(), actor, []uuid.UUID{f.user.ID}, nil)
			return err
		}},
		{"ChangePassword", func(f *cachedFixture) error {
			return f.service.ChangePassword(context.Background(), users.ChangePasswordCommand{
				UserID: f.user.ID, OldPassword: cachedFixturePassword, NewPassword: "a brand new password",
			})
		}},
		{"ResetPassword", func(f *cachedFixture) error {
			_, err := f.service.ResetPassword(context.Background(), actor, f.user.ID)
			return err
		}},
		{"BulkResetPassword", func(f *cachedFixture) error {
			_, err := f.service.BulkResetPassword(context.Background(), actor, []uuid.UUID{f.user.ID})
			return err
		}},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			f := newCachedFixture(t, time.Hour)
			if code, _ := f.serve(f.token); code != http.StatusOK {
				t.Fatalf("before the change: status = %d, want 200", code)
			}
			if code, _ := f.serve(f.token); code != http.StatusOK || f.counted.readCount() != 1 {
				t.Fatalf("the account was not served from the cache before the change (status %d, %d reads)",
					code, f.counted.readCount())
			}

			if err := op.run(f); err != nil {
				t.Fatalf("%s returned error: %v", op.name, err)
			}

			if code, _ := f.serve(f.token); code != http.StatusUnauthorized {
				t.Errorf("the request after %s: status = %d, want 401", op.name, code)
			}
		})
	}
}

func TestAChangeOfPermissionsIsSeenByTheNextSession(t *testing.T) {
	f := newCachedFixture(t, time.Hour)
	if code, identity := f.serve(f.token); code != http.StatusOK || !identity.Has(rbac.PermissionUsersManage) {
		t.Fatalf("before the change: status %d, identity %+v", code, identity)
	}

	if err := f.service.ReplaceRoles(context.Background(), uuid.New(), f.user.ID, nil); err != nil {
		t.Fatalf("ReplaceRoles() returned error: %v", err)
	}
	changed, _ := f.repo.Get(f.user.ID)
	token, err := f.sessions.Create(context.Background(), Principal{
		UserID: changed.ID, Login: changed.Login, Generation: changed.SessionGeneration,
	})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}

	code, identity := f.serve(token)
	if code != http.StatusOK {
		t.Fatalf("the new session: status = %d, want 200", code)
	}
	if identity.Has(rbac.PermissionUsersManage) {
		t.Error("the new session still carries a permission its roles no longer grant")
	}
}

func TestACachedCopyNeverRefusesWhatTheDatabaseWouldAdmit(t *testing.T) {
	f := newCachedFixture(t, time.Hour)
	if code, _ := f.serve(f.token); code != http.StatusOK {
		t.Fatalf("warming request: status = %d, want 200", code)
	}

	generation, err := f.repo.BumpSessionGeneration(context.Background(), f.user.ID)
	if err != nil {
		t.Fatalf("BumpSessionGeneration() returned error: %v", err)
	}
	token, err := f.sessions.Create(context.Background(), Principal{
		UserID: f.user.ID, Login: f.user.Login, Generation: generation,
	})
	if err != nil {
		t.Fatalf("Create() returned error: %v", err)
	}
	reads := f.counted.readCount()

	if code, _ := f.serve(token); code != http.StatusOK {
		t.Errorf("a session of the current generation was refused on a stale copy: status = %d", code)
	}
	if f.counted.readCount() != reads+1 {
		t.Error("the refusal the copy suggested was not checked against the database")
	}
	if _, err := f.sessions.Get(context.Background(), token); err != nil {
		t.Errorf("the new session was discarded: %v", err)
	}
	if code, _ := f.serve(f.token); code != http.StatusUnauthorized {
		t.Errorf("the retired session: status = %d, want 401", code)
	}
}

func TestAChangeTheCacheWasNotToldAboutAppliesWithinItsLifetime(t *testing.T) {
	const ttl = 50 * time.Millisecond
	f := newCachedFixture(t, ttl)
	if code, _ := f.serve(f.token); code != http.StatusOK {
		t.Fatalf("warming request: status = %d, want 200", code)
	}

	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{}); err != nil {
		t.Fatalf("SetStatus() returned error: %v", err)
	}
	time.Sleep(3 * ttl)

	if code, _ := f.serve(f.token); code != http.StatusUnauthorized {
		t.Errorf("one lifetime after an untold block: status = %d, want 401", code)
	}
}

func TestAnUnreachableAccountCacheFallsBackToTheDatabase(t *testing.T) {
	f := newCachedFixture(t, time.Hour)
	if code, _ := f.serve(f.token); code != http.StatusOK {
		t.Fatalf("warming request: status = %d, want 200", code)
	}
	f.store.fail(true)
	reads := f.counted.readCount()

	if code, _ := f.serve(f.token); code != http.StatusOK {
		t.Errorf("with the account cache down: status = %d, want 200", code)
	}
	if f.counted.readCount() != reads+1 {
		t.Error("the account was not read from the database while its cache was down")
	}

	if err := f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{}); err != nil {
		t.Fatalf("SetStatus() returned error: %v", err)
	}
	if code, _ := f.serve(f.token); code != http.StatusUnauthorized {
		t.Errorf("a blocked account with the account cache down: status = %d, want 401", code)
	}
}

func TestAOneTimePasswordAccountIsHeldAtTheDoorFromACachedCopyToo(t *testing.T) {
	f := newCachedFixture(t, time.Hour)
	if err := f.repo.SetPassword(context.Background(), f.user.ID, "digest", true); err != nil {
		t.Fatalf("SetPassword() returned error: %v", err)
	}

	for i := range 3 {
		if code, _ := f.serve(f.token); code != http.StatusForbidden {
			t.Errorf("request %d: status = %d, want 403", i, code)
		}
	}
	if got := f.store.setCount(); got != 1 {
		t.Errorf("three refused requests wrote the account cache %d times, want once", got)
	}
}

func TestSessionStillValidEndsWithTheAccountNotOnlyWithTheSession(t *testing.T) {
	cases := map[string]func(t *testing.T, f *mwFixture){
		"blocked": func(t *testing.T, f *mwFixture) {
			_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusBlocked, users.StatusChange{})
		},
		"deleted": func(t *testing.T, f *mwFixture) {
			_ = f.repo.SetStatus(context.Background(), []uuid.UUID{f.user.ID}, users.StatusDeleted, users.StatusChange{})
		},
		"sessions retired": func(t *testing.T, f *mwFixture) {
			if _, err := f.repo.BumpSessionGeneration(context.Background(), f.user.ID); err != nil {
				t.Fatalf("BumpSessionGeneration() returned error: %v", err)
			}
		},
		"one-time password issued": func(t *testing.T, f *mwFixture) {
			_ = f.repo.SetPassword(context.Background(), f.user.ID, "not-a-real-hash", true)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMiddlewareFixture(t, staticRoles{})
			if alive, err := f.mw.SessionStillValid(authed(f.token)); err != nil || !alive {
				t.Fatalf("before the change SessionStillValid() = %v, %v; want true", alive, err)
			}

			change(t, f)

			alive, err := f.mw.SessionStillValid(authed(f.token))
			if err != nil {
				t.Fatalf("SessionStillValid() returned error: %v", err)
			}
			if alive {
				t.Error("the stream's session was still reported valid after the account changed")
			}
		})
	}
}

func TestSessionStillValidReportsAnUnreadableAccountAsAnError(t *testing.T) {
	f := newMiddlewareFixture(t, staticRoles{})
	f.mw.users = failingUsers{UserStore: f.repo}

	alive, err := f.mw.SessionStillValid(authed(f.token))
	if err == nil {
		t.Fatalf("SessionStillValid() = %v, nil; want the store's error", alive)
	}
}

type failingUsers struct{ UserStore }

func (failingUsers) ByID(context.Context, uuid.UUID) (users.User, error) {
	return users.User{}, errors.New("the database is away")
}
