package api_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	integrationsClientID     = "client-id-7d1e"
	integrationsClientSecret = "client-secret-93ab"
)

type switchCall struct {
	actor    uuid.UUID
	provider string
	enabled  bool
}

// integrationSwitches records the writes it is asked for.
type integrationSwitches struct {
	off   map[string]bool
	err   error
	calls []switchCall
}

func (s *integrationSwitches) DisabledSignInProviders(context.Context) (map[string]bool, error) {
	return s.off, s.err
}

func (s *integrationSwitches) SetSignInProviderEnabled(_ context.Context, actor uuid.UUID, provider string, enabled bool) error {
	s.calls = append(s.calls, switchCall{actor, provider, enabled})
	return nil
}

type integrationsFixture struct {
	router   http.Handler
	switches *integrationSwitches
	admin    uuid.UUID
	cookie   *http.Cookie
	log      *strings.Builder
}

func newIntegrationsFixture(t *testing.T, publicURL string, permissions ...string) *integrationsFixture {
	t.Helper()
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("admin", permissions...)
	admin := repo.Add(users.User{Login: "root", Email: "root@usm.md", Status: users.StatusActive, Roles: []string{"admin"}})

	logs := &strings.Builder{}
	log := logging.New("error", logs)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: admin.ID, Login: admin.Login})
	if err != nil {
		t.Fatalf("session Create() = %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	microsoft := stubProvider("microsoft", &stubConnector{authorize: "https://login.example/" + integrationsClientID + "/" + integrationsClientSecret})
	microsoft.Variables = []string{"MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"}
	google := stubProvider("google", &stubConnector{})
	google.Variables = []string{"GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"}
	github := api.SignInProvider{ID: "github", Variables: []string{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET"}}

	switches := &integrationSwitches{off: map[string]bool{"google": true}}
	router := chi.NewRouter()
	api.NewIntegrationsHandler(api.Integrations{
		Providers: []api.SignInProvider{microsoft, google, github}, PublicURL: publicURL,
		Switches: switches, Middleware: mw, Log: log,
	}).Mount(router)

	return &integrationsFixture{
		router: router, switches: switches, admin: admin.ID,
		cookie: &http.Cookie{Name: sessionName, Value: token}, log: logs,
	}
}

func (f *integrationsFixture) do(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
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

func TestIntegrationsListsEveryProviderInOrder(t *testing.T) {
	f := newIntegrationsFixture(t, "https://contest.example.edu", rbac.PermissionSettingsManage)

	rec := f.do(http.MethodGet, "/integrations", "", f.cookie)

	expectStatus(t, rec, http.StatusOK, "")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	want := `{"providers":[` +
		`{"id":"microsoft","configured":true,"enabled":true,"redirect_uri":"https://contest.example.edu/api/v1/auth/microsoft/callback","variables":["MICROSOFT_CLIENT_ID","MICROSOFT_CLIENT_SECRET"]},` +
		`{"id":"google","configured":true,"enabled":false,"redirect_uri":"https://contest.example.edu/api/v1/auth/google/callback","variables":["GOOGLE_CLIENT_ID","GOOGLE_CLIENT_SECRET"]},` +
		`{"id":"github","configured":false,"enabled":true,"redirect_uri":"https://contest.example.edu/api/v1/auth/github/callback","variables":["GITHUB_CLIENT_ID","GITHUB_CLIENT_SECRET"]}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

func TestTheRedirectURIIsEmptyWithoutAPublicURL(t *testing.T) {
	f := newIntegrationsFixture(t, "", rbac.PermissionSettingsManage)

	rec := f.do(http.MethodGet, "/integrations", "", f.cookie)

	expectStatus(t, rec, http.StatusOK, "")
	if got := strings.Count(rec.Body.String(), `"redirect_uri":""`); got != 3 {
		t.Fatalf("empty redirect_uri count = %d, want 3 (%s)", got, rec.Body.String())
	}
}

func TestIntegrationsNeverAnswersACredential(t *testing.T) {
	f := newIntegrationsFixture(t, "https://contest.example.edu", rbac.PermissionSettingsManage)

	body := f.do(http.MethodGet, "/integrations", "", f.cookie).Body.String()

	for _, secret := range []string{integrationsClientID, integrationsClientSecret} {
		if strings.Contains(body, secret) {
			t.Fatalf("body carries %q: %s", secret, body)
		}
	}
}

func TestIntegrationsNeedSettingsManage(t *testing.T) {
	cases := []struct {
		name, method, path, body string
	}{
		{"list", http.MethodGet, "/integrations", ""},
		{"switch", http.MethodPut, "/integrations/google", `{"enabled":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newIntegrationsFixture(t, "https://contest.example.edu", rbac.PermissionUsersManage)

			expectStatus(t, f.do(tc.method, tc.path, tc.body), http.StatusUnauthorized, "")
			expectStatus(t, f.do(tc.method, tc.path, tc.body, f.cookie), http.StatusForbidden, "")
			if len(f.switches.calls) != 0 {
				t.Fatalf("switch calls = %v, want none", f.switches.calls)
			}
		})
	}
}

func TestSwitchingAProviderOffAndOn(t *testing.T) {
	f := newIntegrationsFixture(t, "", rbac.PermissionSettingsManage)

	for _, body := range []string{`{"enabled":false}`, `{"enabled":true}`} {
		expectStatus(t, f.do(http.MethodPut, "/integrations/google", body, f.cookie), http.StatusNoContent, "")
	}

	want := []switchCall{{f.admin, "google", false}, {f.admin, "google", true}}
	if len(f.switches.calls) != 2 || f.switches.calls[0] != want[0] || f.switches.calls[1] != want[1] {
		t.Fatalf("calls = %v, want %v", f.switches.calls, want)
	}
}

func TestSwitchingAnUnknownOrUnconfiguredProviderIsNotFound(t *testing.T) {
	f := newIntegrationsFixture(t, "", rbac.PermissionSettingsManage)

	for _, id := range []string{"okta", "github"} {
		expectStatus(t, f.do(http.MethodPut, "/integrations/"+id, `{"enabled":true}`, f.cookie), http.StatusNotFound, "not_found")
	}
	if len(f.switches.calls) != 0 {
		t.Fatalf("switch calls = %v, want none", f.switches.calls)
	}
}

func TestASwitchWithoutABooleanIsRefused(t *testing.T) {
	f := newIntegrationsFixture(t, "", rbac.PermissionSettingsManage)

	for _, body := range []string{`{}`, `{"enabled":"no"}`, `{"enabled":false,"x":1}`} {
		expectStatus(t, f.do(http.MethodPut, "/integrations/google", body, f.cookie), http.StatusBadRequest, "invalid_request")
	}
	if len(f.switches.calls) != 0 {
		t.Fatalf("switch calls = %v, want none", f.switches.calls)
	}
}

func TestAnUnreadableSwitchIsAnInternalError(t *testing.T) {
	f := newIntegrationsFixture(t, "", rbac.PermissionSettingsManage)
	f.switches.err = errors.New("database down")

	rec := f.do(http.MethodGet, "/integrations", "", f.cookie)

	expectStatus(t, rec, http.StatusInternalServerError, "internal_error")
	if !strings.Contains(f.log.String(), "database down") {
		t.Errorf("failure not logged: %q", f.log.String())
	}
}
