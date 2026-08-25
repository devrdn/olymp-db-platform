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
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// contestFixture mounts the contest endpoints behind a session for an account
// holding the given installation-wide permissions.
type contestFixture struct {
	router  http.Handler
	service *contests.Service
	stores  *conteststest.Fixture
	actor   users.User
	cookie  *http.Cookie
}

func newContestFixture(t *testing.T, permissions ...string) *contestFixture {
	t.Helper()

	stores := conteststest.NewFixture()
	stores.Users.GrantRole("staff", permissions...)
	actor := stores.Users.Add(users.User{
		Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"},
	})

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: stores.Users,
		Authorizer: rbac.New(contestRoles{stores}),
		Cookies:    auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewContestsHandler(stores.Service, mw, log, "en").Mount(router)

	return &contestFixture{
		router:  router,
		service: stores.Service,
		stores:  stores,
		actor:   actor,
		cookie:  &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

// contestRoles resolves a contest role out of the in-memory staff store, so
// authorisation in these tests runs the real two-level check.
type contestRoles struct {
	stores *conteststest.Fixture
}

func (r contestRoles) ContestRole(ctx context.Context, userID, contestID uuid.UUID) (rbac.ContestRole, error) {
	m, err := r.stores.Managers.Get(ctx, contestID, userID)
	if err != nil {
		return rbac.RoleNone, nil
	}
	return m.Role, nil
}

func (f *contestFixture) do(method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// decode reads a JSON response body into a map.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// errorCode reads the machine code out of an error response.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := decode(t, rec)
	detail, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no error object: %s", rec.Body.String())
	}
	code, _ := detail["code"].(string)
	return code
}

// ownedContest seeds a contest the fixture's actor owns.
func (f *contestFixture) ownedContest(t *testing.T, status string) contests.Contest {
	t.Helper()
	c := f.stores.SeedContest(status)
	if err := f.stores.Managers.Grant(t.Context(), contests.Manager{
		ContestID: c.ID, UserID: f.actor.ID, Role: rbac.RoleOwner, GrantedBy: f.actor.ID,
	}); err != nil {
		t.Fatalf("Grant() returned error: %v", err)
	}
	return c
}

func TestCreatingAContestReturnsIt(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestCreate)

	rec := f.do(http.MethodPost, "/contests", `{
		"languages": [{"code": "en", "is_default": true}],
		"translations": {"en": {"title": "The Library Murder"}}
	}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["status"] != contests.StatusDraft {
		t.Errorf("status = %v, want draft", body["status"])
	}
}

func TestCreatingAContestNeedsThePermission(t *testing.T) {
	f := newContestFixture(t)

	rec := f.do(http.MethodPost, "/contests", `{}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestReadingAContestNeedsToStaffIt(t *testing.T) {
	// The second level of authorisation: an organizer who did not create this
	// contest has no business reading its answers.
	f := newContestFixture(t, rbac.PermissionContestCreate)
	other := f.stores.SeedContest(contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+other.ID.String(), "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestStaffSeeEveryTranslationOfTheirContest(t *testing.T) {
	// They are authoring them: showing only the negotiated one would make the
	// others invisible in the editor.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	if err := f.service.SetLanguages(t.Context(), f.actor.ID, c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}); err != nil {
		t.Fatalf("SetLanguages() returned error: %v", err)
	}
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
		{Lang: "ro", Title: "Crima din bibliotecă"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String(), "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	translations, ok := decode(t, rec)["translations"].(map[string]any)
	if !ok || len(translations) != 2 {
		t.Errorf("translations = %v, want both languages", translations)
	}
}

func TestListingAnswersInTheRequestedLanguage(t *testing.T) {
	// The title lives only in the translations, so a listing has to negotiate
	// one — this is the single place that decision is made (§6.2).
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	// The contest has to offer the language, not merely have text in it: a
	// title in Romanian over a story in English is the mixture the declared
	// set exists to prevent.
	if err := f.service.SetLanguages(t.Context(), f.actor.ID, c.ID,
		[]contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}}); err != nil {
		t.Fatalf("SetLanguages() returned error: %v", err)
	}
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
		{Lang: "ro", Title: "Crima din bibliotecă"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests?lang=ro", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	items, _ := decode(t, rec)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no contests listed: %s", rec.Body.String())
	}
	first, _ := items[0].(map[string]any)
	if first["title"] != "Crima din bibliotecă" {
		t.Errorf("title = %v, want the Romanian one", first["title"])
	}
}

func TestListingFallsBackWhenTheRequestedLanguageIsMissing(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)
	if err := f.service.SetTranslations(t.Context(), f.actor.ID, c.ID, []contests.Translation{
		{Lang: "en", Title: "The Library Murder"},
	}); err != nil {
		t.Fatalf("SetTranslations() returned error: %v", err)
	}

	rec := f.do(http.MethodGet, "/contests?lang=ru", "")

	items, _ := decode(t, rec)["items"].([]any)
	first, _ := items[0].(map[string]any)
	if first["title"] != "The Library Murder" {
		t.Errorf("title = %v, want the fallback language", first["title"])
	}
	if first["lang"] != "en" {
		t.Errorf("lang = %v, want the language actually served", first["lang"])
	}
}

func TestPublishCheckListsWhatIsMissing(t *testing.T) {
	// The constructor screen shows the remaining work rather than making an
	// organizer discover it by being refused.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodGet, "/contests/"+c.ID.String()+"/publish-check", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["ready"] != false {
		t.Errorf("ready = %v, want false", body["ready"])
	}
	problems, _ := body["problems"].([]any)
	if len(problems) == 0 {
		t.Errorf("problems = %v, want the missing story and questions named", problems)
	}
}

func TestPublishingAnIncompleteContestIsRefusedWithItsReasons(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/status", `{"status": "published"}`)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "not_publishable" {
		t.Errorf("error code = %q, want not_publishable", code)
	}
}

func TestAnImpossibleTransitionIsAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPost, "/contests/"+c.ID.String()+"/status", `{"status": "running"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestEditingAFinishedContestIsAConflict(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusFinished)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"enrollment": "open"}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnsafeWritableTableIsRejected(t *testing.T) {
	// These names end up in GRANT statements, so the refusal has to reach the
	// organizer as a bad request rather than a 500 from the database.
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPut, "/contests/"+c.ID.String()+"/sql-policy", `{
		"mode": "read_write",
		"writable_tables": ["evidence; DROP TABLE users"],
		"disk_quota_ratio": 5
	}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAnUnknownContestIsNotFound(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet, "/contests/"+uuid.NewString(), "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAMalformedContestIdentifierIsABadRequest(t *testing.T) {
	f := newContestFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(http.MethodGet, "/contests/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestNetworkRestrictionsAreReadBackAsWritten(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(),
		`{"allowed_cidrs": ["10.20.0.0/16", "192.168.1.42/32"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	cidrs, _ := decode(t, rec)["allowed_cidrs"].([]any)
	if len(cidrs) != 2 || cidrs[0] != "10.20.0.0/16" {
		t.Errorf("allowed_cidrs = %v, want both networks", cidrs)
	}
}

func TestAMalformedNetworkIsRejected(t *testing.T) {
	f := newContestFixture(t)
	c := f.ownedContest(t, contests.StatusDraft)

	rec := f.do(http.MethodPatch, "/contests/"+c.ID.String(), `{"allowed_cidrs": ["10.20.0.0/64"]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}
