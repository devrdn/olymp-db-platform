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
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type settingsFixture struct {
	router http.Handler
	store  *settingsRepo
	cookie *http.Cookie
}

// settingsRepo is the storage, in memory. The service itself is the real one:
// a hand-written stand-in would have its own idea of which keys are known and
// which values are valid, and the endpoint's job is to carry the real answers.
type settingsRepo struct{ values settings.Values }

func (r *settingsRepo) All(context.Context) (settings.Values, error) {
	out := settings.Values{}
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *settingsRepo) Save(_ context.Context, _ uuid.UUID, values settings.Values) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

type settingsUnitOfWork struct{}

func (settingsUnitOfWork) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func newSettingsFixture(t *testing.T, permissions ...string) *settingsFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("admin", permissions...)
	admin := repo.Add(users.User{
		Login: "root", FullName: "Root", Status: users.StatusActive, Roles: []string{"admin"},
	})

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: admin.ID, Login: admin.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	store := &settingsRepo{values: settings.Values{}}
	service := settings.NewService(store, audit.New(&apiSink{}), settingsUnitOfWork{})

	router := chi.NewRouter()
	api.NewSettingsHandler(service, mw, log).Mount(router)

	return &settingsFixture{
		router: router,
		store:  store,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *settingsFixture) do(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func settingsOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Values
}

func TestTheInstallationNamesItselfToAVisitorWithNoSession(t *testing.T) {
	// The sign-in screen carries it, and that screen is seen before anybody
	// has signed in. An endpoint behind the session would leave the first page
	// of the product unable to say what the product is.
	f := newSettingsFixture(t)

	rec := f.do(http.MethodGet, "/settings", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if settingsOf(t, rec)[settings.KeyName] == "" {
		t.Error("the public read carries no name")
	}
}

func TestThePublicReadPublishesOnlyWhatWasDeclaredPublic(t *testing.T) {
	// The reason it is an allow-list and not the table. A setting nobody
	// marked public must not leave, whatever ends up stored beside it — the
	// day somebody adds a mail server's password, an endpoint returning
	// everything would publish it, and nothing in that change would look like
	// a disclosure.
	f := newSettingsFixture(t)
	f.store.values["installation.smtp_password"] = "hunter2"

	rec := f.do(http.MethodGet, "/settings", "")

	if _, leaked := settingsOf(t, rec)["installation.smtp_password"]; leaked {
		t.Errorf("an undeclared setting was published: %s", rec.Body.String())
	}
}

func TestChangingTheInstallationNeedsThePermissionToDoIt(t *testing.T) {
	// Naming the university is not something contest staff do; an organizer
	// who could would be rebranding an installation from inside a contest they
	// happen to run.
	f := newSettingsFixture(t)

	rec := f.do(http.MethodPut, "/settings",
		`{"values":{"installation.name":"Somewhere Else"}}`, f.cookie)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestEveryValueIsReadableOnlyByAnAdministrator(t *testing.T) {
	// The full read is separate from the public one for the same reason the
	// public one is an allow-list: everything, to anyone, is how a private
	// setting becomes a published one.
	f := newSettingsFixture(t)

	if rec := f.do(http.MethodGet, "/settings/all", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without a session", rec.Code)
	}
	if rec := f.do(http.MethodGet, "/settings/all", "", f.cookie); rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without the permission", rec.Code)
	}
}

func TestAnAdministratorSavesAndGetsTheResultBack(t *testing.T) {
	f := newSettingsFixture(t, rbac.PermissionSettingsManage)

	rec := f.do(http.MethodPut, "/settings", `{"values":{
		"installation.name": "Universitatea Tehnică",
		"installation.contact_email": "olimpiada@example.edu"
	}}`, f.cookie)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if settingsOf(t, rec)[settings.KeyName] != "Universitatea Tehnică" {
		t.Errorf("the response does not carry what was saved: %s", rec.Body.String())
	}
}

func TestASettingNothingReadsIsRefusedByTheAPI(t *testing.T) {
	f := newSettingsFixture(t, rbac.PermissionSettingsManage)

	rec := f.do(http.MethodPut, "/settings", `{"values":{"instalation.name":"typo"}}`, f.cookie)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}
