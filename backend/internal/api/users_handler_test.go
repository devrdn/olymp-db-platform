package api_test

import (
	"encoding/json"
	"fmt"
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
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type apiFixture struct {
	router http.Handler
	repo   *userstest.Repository
	admin  users.User
	cookie *http.Cookie
}

// newAPIFixture mounts the account endpoints behind a session for an account
// holding the given permissions.
func newAPIFixture(t *testing.T, permissions ...string) *apiFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("admin", permissions...)
	admin := repo.Add(users.User{Login: "root", FullName: "Root", Status: users.StatusActive, Roles: []string{"admin"}})

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
	service := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{})

	router := chi.NewRouter()
	api.NewUsersHandler(service, repo, mw, log).Mount(router)

	return &apiFixture{
		router: router,
		repo:   repo,
		admin:  admin,
		cookie: &http.Cookie{Name: auth.SessionCookieName, Value: token},
	}
}

func (f *apiFixture) do(method, path, body string) *httptest.ResponseRecorder {
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

func TestCreateEndpointReturnsTheOneTimePassword(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users", `{"login":"petrov","full_name":"Pyotr Petrov","roles":["student"]}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		User            map[string]any `json:"user"`
		OneTimePassword string         `json:"one_time_password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.OneTimePassword == "" {
		t.Error("the response carries no password to hand over")
	}
}

func TestAccountResponsesNeverCarryThePasswordDigest(t *testing.T) {
	// The single most damaging field to leak, and the easiest to leak by
	// serialising the domain object directly.
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", PasswordHash: "$argon2id$secret"})

	for _, rec := range []*httptest.ResponseRecorder{
		f.do(http.MethodGet, "/users", ""),
		f.do(http.MethodPost, "/users", `{"login":"sidorov","full_name":"Sidor Sidorov"}`),
	} {
		if strings.Contains(rec.Body.String(), "argon2id") || strings.Contains(rec.Body.String(), "password_hash") {
			t.Errorf("a response leaks the stored digest: %s", rec.Body.String())
		}
	}
}

func TestAccountEndpointsRequireThePermission(t *testing.T) {
	// A signed-in student must not reach account management.
	f := newAPIFixture(t, rbac.PermissionReportsView)

	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/users", ""},
		{http.MethodPost, "/users", `{"login":"petrov","full_name":"Pyotr"}`},
		{http.MethodPost, "/users/" + uuid.NewString() + "/block", ""},
		{http.MethodPost, "/users/" + uuid.NewString() + "/password-reset", ""},
		{http.MethodPut, "/users/" + uuid.NewString() + "/roles", `{"roles":["student"]}`},
	}
	for _, c := range cases {
		if rec := f.do(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}
}

func TestAccountEndpointsRequireASession(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	rec := httptest.NewRecorder()

	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestListEndpointPagesTheAccounts(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	for i := range 5 {
		f.repo.Add(users.User{Login: "user" + string(rune('a'+i)), FullName: "User"})
	}

	rec := f.do(http.MethodGet, "/users?limit=2", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body.Items) != 2 {
		t.Errorf("returned %d items, want the requested 2", len(body.Items))
	}
	if body.Total < 5 {
		t.Errorf("total = %d, want it to count every match, not just the page", body.Total)
	}
}

func TestBlockEndpointBlocksTheAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/block", `{"reason":"cheating in the October contest"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if stored.Status != users.StatusBlocked {
		t.Errorf("Status = %q, want blocked", stored.Status)
	}
	if stored.StatusReason != "cheating in the October contest" {
		t.Errorf("StatusReason = %q, want the reason from the request", stored.StatusReason)
	}
}

func TestBlockingYourOwnAccountIsRefused(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users/"+f.admin.ID.String()+"/block", `{"reason":"some reason"}`)

	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want the self-block to be refused", rec.Code)
	}
}

func TestPasswordResetEndpointReturnsTheNewPassword(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/password-reset", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		OneTimePassword string `json:"one_time_password"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.OneTimePassword == "" {
		t.Error("the reset returned no password to hand over")
	}
}

func TestUnknownAccountIsReportedAsNotFound(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users/"+uuid.NewString()+"/block", `{"reason":"some reason"}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestMalformedAccountIDIsRejected(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users/not-a-uuid/block", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRolesEndpointReplacesTheSet(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Roles: []string{"student"}})

	rec := f.do(http.MethodPut, "/users/"+target.ID.String()+"/roles", `{"roles":["organizer"]}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if len(stored.Roles) != 1 || stored.Roles[0] != "organizer" {
		t.Errorf("Roles = %v, want [organizer]", stored.Roles)
	}
}

func TestImportEndpointReturnsAPasswordForEveryAccountItCreated(t *testing.T) {
	// The passwords are shown once, here. An import that created accounts and
	// did not return them would leave thirty people unable to sign in and no
	// way to find out what their password was.
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users/import", `{
		"roles": ["student"],
		"rows": [
			{"login": "s.popescu", "full_name": "Sergiu Popescu"},
			{"login": "i.ivanov", "full_name": "Ivan Ivanov", "email": "i@example.edu"}
		]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Created []struct {
			User            UserRef `json:"user"`
			OneTimePassword string  `json:"one_time_password"`
		} `json:"created"`
		Skipped []struct {
			Login  string `json:"login"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(body.Created) != 2 {
		t.Fatalf("created %d accounts, want 2", len(body.Created))
	}
	for _, created := range body.Created {
		if created.OneTimePassword == "" {
			t.Errorf("%s came back without a password to hand over", created.User.Login)
		}
	}
}

func TestImportEndpointReportsTheRowsItSkipped(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	f.repo.Add(users.User{Login: "s.popescu", FullName: "Sergiu Popescu", Status: users.StatusActive})

	rec := f.do(http.MethodPost, "/users/import",
		`{"rows": [{"login": "s.popescu", "full_name": "Sergiu Popescu"}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Skipped []struct {
			Login  string `json:"login"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(body.Skipped) != 1 || body.Skipped[0].Reason != users.SkipLoginTaken {
		t.Errorf("skipped = %+v, want the duplicate named", body.Skipped)
	}
}

func TestImportEndpointRefusesAnOversizedRoster(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rows := make([]string, 501)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"login":"s%d","full_name":"Student"}`, i)
	}

	rec := f.do(http.MethodPost, "/users/import", `{"rows": [`+strings.Join(rows, ",")+`]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestImportEndpointNeedsThePermission(t *testing.T) {
	f := newAPIFixture(t)

	rec := f.do(http.MethodPost, "/users/import", `{"rows": []}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// UserRef is the slice of the account response these tests read.
type UserRef struct {
	Login string `json:"login"`
}

func TestRolesEndpointPublishesTheRoleCatalogue(t *testing.T) {
	// The interface has to offer roles when creating an account, and the codes
	// are rows in a table rather than an enum — the whole point of the
	// permission model is that a new role is data (section 11). Hard-coding
	// "student, organizer, admin" in the frontend would quietly undo that: the
	// day somebody adds a role, one of the two lists is wrong and neither says
	// so.
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodGet, "/roles", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body.Items) == 0 {
		t.Fatal("the catalogue is empty; the interface has nothing to offer")
	}
	for _, item := range body.Items {
		if item.Code == "" || item.Name == "" {
			t.Errorf("role %+v is missing a code or a name", item)
		}
	}
}

func TestRolesEndpointIsClosedToAnAccountThatCannotManageAccounts(t *testing.T) {
	// Which roles exist is a description of how this installation is
	// organised. It goes with the screen that uses it and with nothing else.
	f := newAPIFixture(t)

	rec := f.do(http.MethodGet, "/roles", "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAnUnknownAccountStatusIsRefusedRatherThanIgnored(t *testing.T) {
	// The listing filters by an exact status, so an unreadable one matches
	// nothing and the screen shows an empty register — "no account matches",
	// which is a true answer to a question nobody asked. The contest listing
	// already refuses an unreadable `enrolled` for exactly this reason, and
	// two endpoints answering the same mistake differently is worse than
	// either choice.
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodGet, "/users?status=banished", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestTheStatusesTheRegisterActuallyOffersAreAccepted(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	for _, status := range []string{"", "active", "blocked"} {
		rec := f.do(http.MethodGet, "/users?status="+status, "")
		if rec.Code != http.StatusOK {
			t.Errorf("status=%q = %d, want 200", status, rec.Code)
		}
	}
}

func TestCreateEndpointAnswersABadRequestForUnusableDetails(t *testing.T) {
	// An empty login is the client's mistake and is told so; it used to come
	// back as a 500 because the error had no name the handler could map.
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users", `{"login":"","full_name":"Nobody"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}
