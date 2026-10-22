package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type apiFixture struct {
	router   http.Handler
	repo     *userstest.Repository
	admin    users.User
	cookie   *http.Cookie
	hasher   *password.Hasher
	unlocker *recordingUnlocker
}

// newAPIFixture mounts the account endpoints behind a session for an account
// holding the given permissions.
func newAPIFixture(t *testing.T, permissions ...string) *apiFixture {
	t.Helper()
	return newAPIFixtureWithMail(t, nil, permissions...)
}

// newAPIFixtureWithMail is newAPIFixture with the service's mail wired, or
// none when mail is nil.
func newAPIFixtureWithMail(t *testing.T, mail *users.Mail, permissions ...string) *apiFixture {
	t.Helper()

	c := cache.NewMemory(1000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("admin", permissions...)
	repo.GrantRole("student")
	repo.GrantRole("organizer")
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
	hasher := passwordtest.NewHasher()
	service := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, hasher)
	if mail != nil {
		service.WithMail(*mail)
	}
	unlocker := &recordingUnlocker{}

	router := chi.NewRouter()
	api.NewUsersHandler(service, repo, unlocker, mw, log).Mount(router)

	return &apiFixture{
		router:   router,
		repo:     repo,
		admin:    admin,
		cookie:   &http.Cookie{Name: auth.SessionCookieName, Value: token},
		hasher:   hasher,
		unlocker: unlocker,
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

func TestBlockWithoutAReasonIsABadRequest(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/block", `{"reason":"  "}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "reason_required" {
		t.Errorf("code = %q, want reason_required", code)
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

func TestDeleteEndpointDeletesTheAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/delete", `{"reason":"graduated"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if stored.Status != users.StatusDeleted {
		t.Errorf("Status = %q, want deleted", stored.Status)
	}
	if stored.StatusReason != "graduated" {
		t.Errorf("StatusReason = %q, want the reason from the request", stored.StatusReason)
	}
}

func TestDeleteWithoutAReasonIsABadRequest(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/delete", `{"reason":"  "}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "reason_required" {
		t.Errorf("code = %q, want reason_required", code)
	}
}

func TestRestoreEndpointRestoresTheAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Status: users.StatusDeleted})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/restore", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	stored, _ := f.repo.Get(target.ID)
	if stored.Status != users.StatusActive {
		t.Errorf("Status = %q, want active", stored.Status)
	}
}

func TestByIDReportsWhoAndWhyForAStatusChangeAndNothingForAFreshAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr"})
	fresh := f.repo.Add(users.User{Login: "sidorov", FullName: "Sidor"})

	if rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/block", `{"reason":"cheating in the October contest"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("block status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}

	rec := f.do(http.MethodGet, "/users/"+target.ID.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		StatusReason    string `json:"status_reason"`
		StatusChangedAt string `json:"status_changed_at"`
		StatusChangedBy string `json:"status_changed_by"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.StatusReason != "cheating in the October contest" {
		t.Errorf("status_reason = %q, want the reason the block was given", body.StatusReason)
	}
	if body.StatusChangedAt == "" {
		t.Error("status_changed_at is empty, want the moment the block landed")
	}
	if body.StatusChangedBy != f.admin.ID.String() {
		t.Errorf("status_changed_by = %q, want the blocking administrator %q", body.StatusChangedBy, f.admin.ID.String())
	}

	// An account never blocked or deleted carries no empty history.
	rec = f.do(http.MethodGet, "/users/"+fresh.ID.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "status_reason") || strings.Contains(rec.Body.String(), "status_changed_at") {
		t.Errorf("a fresh account's response names a status change that never happened: %s", rec.Body.String())
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

// TestSingleAccountOperationsRefuseADeletedAccount matches the bulk endpoints,
// which skip a deleted account (SkipDeleted).
func TestSingleAccountOperationsRefuseADeletedAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "gone", FullName: "Gone Petrov", Status: users.StatusDeleted})

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"profile", http.MethodPatch, "/users/" + target.ID.String(), `{"full_name":"New Name"}`},
		{"password reset", http.MethodPost, "/users/" + target.ID.String() + "/password-reset", ""},
		{"roles", http.MethodPut, "/users/" + target.ID.String() + "/roles", `{"roles":["student"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.do(c.method, c.path, c.body)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body: %s)", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != "account_deleted" {
				t.Errorf("code = %q, want account_deleted", code)
			}
		})
	}
}

func TestRolesEndpointAnswersAnUnknownRoleWith400(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Roles: []string{"student"}})

	rec := f.do(http.MethodPut, "/users/"+target.ID.String()+"/roles", `{"roles":["studnet"]}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "unknown_role" {
		t.Errorf("code = %q, want unknown_role", code)
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
	// The passwords are shown once, here; an import that did not return them
	// would leave its accounts unusable.
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
	// Role codes are table rows, not an enum: a new role is data. A list
	// hard-coded in the frontend would go silently wrong the day one is added.
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
	// Which roles exist describes how the installation is organised; it goes
	// only with the screen that uses it.
	f := newAPIFixture(t)

	rec := f.do(http.MethodGet, "/roles", "")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestAnUnknownAccountStatusIsRefusedRatherThanIgnored(t *testing.T) {
	// The listing filters by exact status, so an unreadable one would show an
	// empty register as if nothing matched. The contest listing refuses an
	// unreadable `enrolled` the same way.
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
	// An empty login is the client's mistake, not a 500.
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users", `{"login":"","full_name":"Nobody"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestCreateEndpointReportsBusyHashingWith503(t *testing.T) {
	// Issuing a password waits for a hashing slot longer than a sign-in does,
	// but once the caller's deadline passes the answer is the declared "busy",
	// never a 500.
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	for range 4 { // passwordtest.NewHasher's concurrency
		slot, err := f.hasher.Hold(context.Background())
		if err != nil {
			t.Fatalf("Hold() returned error: %v", err)
		}
		t.Cleanup(slot.Release)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/users",
		strings.NewReader(`{"login":"petrov","full_name":"Pyotr Petrov","roles":["student"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body: %s)", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "sign_in_busy" {
		t.Errorf("code = %q, want sign_in_busy", code)
	}
}

// recordingUnlocker stands in for auth.Service's UnlockSignIn.
type recordingUnlocker struct {
	actor, user uuid.UUID
	calls       int
	err         error
}

func (u *recordingUnlocker) UnlockSignIn(_ context.Context, actorID, userID uuid.UUID) error {
	u.actor, u.user = actorID, userID
	u.calls++
	return u.err
}

func TestSignInUnlockEndpointClearsTheAccountsThrottleAs204(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	target := uuid.New()

	rec := f.do(http.MethodPost, "/users/"+target.String()+"/sign-in/unlock", "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	if f.unlocker.calls != 1 || f.unlocker.user != target || f.unlocker.actor != f.admin.ID {
		t.Errorf("unlocker = %+v, want one call for %v by the administrator %v", f.unlocker, target, f.admin.ID)
	}
}

func TestSignInUnlockEndpointAnswers404ForAnUnknownAccount(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	f.unlocker.err = users.ErrNotFound

	rec := f.do(http.MethodPost, "/users/"+uuid.NewString()+"/sign-in/unlock", "")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestSignInUnlockEndpointRefusesAMalformedID(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users/not-a-uuid/sign-in/unlock", "")

	if rec.Code != http.StatusBadRequest || f.unlocker.calls != 0 {
		t.Errorf("status = %d, calls = %d, want 400 and no unlock", rec.Code, f.unlocker.calls)
	}
}

func TestSignInUnlockEndpointNeedsThePermission(t *testing.T) {
	f := newAPIFixture(t, rbac.PermissionReportsView)

	rec := f.do(http.MethodPost, "/users/"+uuid.NewString()+"/sign-in/unlock", "")

	if rec.Code != http.StatusForbidden || f.unlocker.calls != 0 {
		t.Errorf("status = %d, calls = %d, want 403 and no unlock", rec.Code, f.unlocker.calls)
	}
}

// slotTakingUsers takes every slot of the hasher once the first account is
// stored, so the rows of an import after it find the hasher at capacity.
type slotTakingUsers struct {
	*userstest.Repository
	hasher *password.Hasher
	t      *testing.T
	taken  bool
}

func (r *slotTakingUsers) Create(ctx context.Context, u users.User) (users.User, error) {
	created, err := r.Repository.Create(ctx, u)
	if !r.taken {
		r.taken = true
		for range r.hasher.Concurrency() {
			slot, holdErr := r.hasher.Hold(context.Background())
			if holdErr != nil {
				r.t.Fatalf("Hold() returned error: %v", holdErr)
			}
			r.t.Cleanup(slot.Release)
		}
	}
	return created, err
}

func TestAnImportStoppedForLoadStillHandsOverThePasswordsItIssued(t *testing.T) {
	// Accounts created before the hasher ran out exist, and their one-time
	// passwords are shown here or never. The answer carries them and names the
	// rows never tried, so those can be imported again.
	f := newAPIFixture(t, rbac.PermissionUsersManage)
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1, MaxWait: 20 * time.Millisecond})
	repo := &slotTakingUsers{Repository: f.repo, hasher: hasher, t: t}
	service := users.NewService(repo, audit.New(&apiSink{}), &userstest.SpyUnitOfWork{}, hasher)
	log := logging.New("error", io.Discard)
	c := cache.NewMemory(100)
	t.Cleanup(func() { _ = c.Close() })
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: f.admin.ID, Login: f.admin.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: f.repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})
	router := chi.NewRouter()
	api.NewUsersHandler(service, f.repo, &recordingUnlocker{}, mw, log).Mount(router)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/users/import", strings.NewReader(`{
		"roles": ["student"],
		"rows": [
			{"login": "s.popescu", "full_name": "Sergiu Popescu"},
			{"login": "i.ivanov", "full_name": "Ivan Ivanov"},
			{"login": "a.rusu", "full_name": "Ana Rusu"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with the partial result (body: %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Created []struct {
			User            UserRef `json:"user"`
			OneTimePassword string  `json:"one_time_password"`
		} `json:"created"`
		NotImported []string `json:"not_imported"`
		Stopped     string   `json:"stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(body.Created) != 1 || body.Created[0].User.Login != "s.popescu" || body.Created[0].OneTimePassword == "" {
		t.Errorf("created = %+v, want s.popescu with a password to hand over", body.Created)
	}
	if strings.Join(body.NotImported, ",") != "i.ivanov,a.rusu" {
		t.Errorf("not_imported = %v, want [i.ivanov a.rusu]", body.NotImported)
	}
	if body.Stopped != "sign_in_busy" {
		t.Errorf("stopped = %q, want sign_in_busy", body.Stopped)
	}
}

// memoryQueue is the outbox's table.
type memoryQueue struct {
	messages []mailing.Message
}

func (q *memoryQueue) Insert(_ context.Context, m mailing.Message) error {
	q.messages = append(q.messages, m)
	return nil
}

func (q *memoryQueue) InsertMany(_ context.Context, ms []mailing.Message) error {
	q.messages = append(q.messages, ms...)
	return nil
}

// AbandonPending drops the accounts' messages, as the table marks them given
// up.
func (q *memoryQueue) AbandonPending(_ context.Context, kind mailing.Kind, accounts []uuid.UUID, _ string) error {
	kept := q.messages[:0]
	for _, m := range q.messages {
		if m.Kind != kind || !slices.Contains(accounts, m.UserID) {
			kept = append(kept, m)
		}
	}
	q.messages = kept
	return nil
}

type fixedMailSettings settings.Values

func (s fixedMailSettings) All(context.Context) (settings.Values, error) {
	return settings.Values(s), nil
}

// newEmailingFixture mounts the account endpoints with mail on, queueing into
// the returned table, or with mail off when on is false.
func newEmailingFixture(t *testing.T, on bool) (*apiFixture, *memoryQueue) {
	t.Helper()
	queue := &memoryQueue{}
	outbox := mailing.Disabled(queue)
	if on {
		outbox = mailing.NewOutbox(queue)
	}
	f := newAPIFixtureWithMail(t, &users.Mail{
		Outbox: outbox,
		Texts:  mailing.NewTexts("https://olympdb.usm.md"),
		Settings: fixedMailSettings{
			settings.KeyName:         "Olimpiada USM",
			settings.KeyMailLanguage: string(mailing.Romanian),
		},
	}, rbac.PermissionUsersManage)
	return f, queue
}

// emailed is the part of an issued password's answer that says whether it
// was emailed.
type emailed struct {
	Login           string  `json:"login"`
	User            UserRef `json:"user"`
	OneTimePassword string  `json:"one_time_password"`
	Emailed         bool    `json:"emailed"`
	// SkippedReason is a pointer so a test can tell an absent key from "".
	SkippedReason *string `json:"email_skipped_reason"`
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// assertQueuedTo checks that exactly one message went to the address, carrying
// the password the administrator was handed.
func assertQueuedTo(t *testing.T, queue *memoryQueue, to, password string) {
	t.Helper()
	found := 0
	for _, m := range queue.messages {
		if m.To != to {
			continue
		}
		found++
		if !strings.Contains(m.Body, password) {
			t.Errorf("the message to %s does not carry the password the answer gave", to)
		}
	}
	if found != 1 {
		t.Errorf("%d messages went to %s, want 1", found, to)
	}
}

func TestCreateEndpointEmailsTheSignInDetailsWhenAsked(t *testing.T) {
	f, queue := newEmailingFixture(t, true)

	rec := f.do(http.MethodPost, "/users",
		`{"login":"a.rusu","full_name":"Ana Rusu","email":"ana@usm.md","send_by_email":true}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[emailed](t, rec)
	if !body.Emailed || body.SkippedReason != nil {
		t.Errorf("emailed = %v, reason = %v; want emailed with no reason", body.Emailed, body.SkippedReason)
	}
	if body.OneTimePassword == "" {
		t.Error("the administrator was not handed the password")
	}
	assertQueuedTo(t, queue, "ana@usm.md", body.OneTimePassword)
}

func TestCreateEndpointNamesAnAccountWithNoAddress(t *testing.T) {
	f, queue := newEmailingFixture(t, true)

	rec := f.do(http.MethodPost, "/users", `{"login":"a.rusu","full_name":"Ana Rusu","send_by_email":true}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[emailed](t, rec)
	if body.Emailed || body.SkippedReason == nil || *body.SkippedReason != "no_address" {
		t.Errorf("emailed = %v, reason = %v; want not emailed for no_address", body.Emailed, body.SkippedReason)
	}
	if body.OneTimePassword == "" {
		t.Error("the administrator was not handed the password")
	}
	if len(queue.messages) != 0 {
		t.Errorf("queued %d messages for an account with no address", len(queue.messages))
	}
}

func TestCreateEndpointEmailsNothingUnlessAsked(t *testing.T) {
	f, queue := newEmailingFixture(t, true)

	rec := f.do(http.MethodPost, "/users", `{"login":"a.rusu","full_name":"Ana Rusu","email":"ana@usm.md"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"emailed":false`) || strings.Contains(rec.Body.String(), "email_skipped_reason") {
		t.Errorf("body = %s, want emailed false and no reason", rec.Body.String())
	}
	if len(queue.messages) != 0 {
		t.Errorf("queued %d messages unasked", len(queue.messages))
	}
}

func TestEveryRouteThatIssuesAPasswordRefusesEmailWhileMailIsOff(t *testing.T) {
	f, _ := newEmailingFixture(t, false)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md",
		PasswordHash: "unchanged"})

	for _, route := range []struct{ name, path, body string }{
		{"create", "/users", `{"login":"new","full_name":"New One","email":"new@usm.md","send_by_email":true}`},
		{"import", "/users/import", `{"rows":[{"login":"new","full_name":"New One"}],"send_by_email":true}`},
		{"reset", "/users/" + target.ID.String() + "/password-reset", `{"send_by_email":true}`},
		{"bulk reset", "/users/bulk/password-reset", `{"ids":["` + target.ID.String() + `"],"send_by_email":true}`},
	} {
		t.Run(route.name, func(t *testing.T) {
			rec := f.do(http.MethodPost, route.path, route.body)

			if rec.Code != http.StatusConflict || errorCode(t, rec) != "mail_off" {
				t.Fatalf("answer = %d %s, want 409 mail_off", rec.Code, rec.Body.String())
			}
			if _, err := f.repo.ByLogin(context.Background(), "new"); err == nil {
				t.Error("an account was created although the request was refused")
			}
			if after, _ := f.repo.Get(target.ID); after.PasswordHash != "unchanged" {
				t.Error("the password was reset although the request was refused")
			}
		})
	}
}

func TestImportEndpointEmailsEachCreatedAccountWithAnAddress(t *testing.T) {
	f, queue := newEmailingFixture(t, true)

	rec := f.do(http.MethodPost, "/users/import", `{
		"send_by_email": true,
		"rows": [
			{"login": "s.popescu", "full_name": "Sergiu Popescu", "email": "sergiu@usm.md"},
			{"login": "i.ivanov", "full_name": "Ivan Ivanov"}
		]
	}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[struct {
		Created []emailed `json:"created"`
	}](t, rec)
	if len(body.Created) != 2 {
		t.Fatalf("created %d accounts, want 2", len(body.Created))
	}
	for _, c := range body.Created {
		switch c.User.Login {
		case "s.popescu":
			if !c.Emailed || c.SkippedReason != nil {
				t.Errorf("s.popescu reads emailed %v, reason %v", c.Emailed, c.SkippedReason)
			}
			assertQueuedTo(t, queue, "sergiu@usm.md", c.OneTimePassword)
		case "i.ivanov":
			if c.Emailed || c.SkippedReason == nil || *c.SkippedReason != "no_address" {
				t.Errorf("i.ivanov reads emailed %v, reason %v; want no_address", c.Emailed, c.SkippedReason)
			}
		}
	}
	if len(queue.messages) != 1 {
		t.Errorf("queued %d messages, want 1", len(queue.messages))
	}
}

func TestPasswordResetEndpointEmailsTheNewPasswordWhenAsked(t *testing.T) {
	f, queue := newEmailingFixture(t, true)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md"})

	rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/password-reset", `{"send_by_email":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decodeJSON[emailed](t, rec)
	if !body.Emailed || body.SkippedReason != nil {
		t.Errorf("emailed = %v, reason = %v; want emailed", body.Emailed, body.SkippedReason)
	}
	assertQueuedTo(t, queue, "petrov@usm.md", body.OneTimePassword)
}

func TestPasswordResetEndpointRefusesABodyItCannotRead(t *testing.T) {
	f, queue := newEmailingFixture(t, true)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md",
		PasswordHash: "unchanged"})

	for _, body := range []string{`{"send_by_email":"yes"}`, `{"send_by_mail":true}`, `{`} {
		rec := f.do(http.MethodPost, "/users/"+target.ID.String()+"/password-reset", body)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
	if after, _ := f.repo.Get(target.ID); after.PasswordHash != "unchanged" || len(queue.messages) != 0 {
		t.Error("a refused body still reset the password or queued a message")
	}
}

func TestAMessageThatCannotBeWrittenIsAServerError(t *testing.T) {
	queue := &memoryQueue{}
	f := newAPIFixtureWithMail(t, &users.Mail{
		Outbox:   mailing.NewOutbox(queue),
		Texts:    mailing.NewTexts("https://olympdb.usm.md"),
		Settings: fixedMailSettings{settings.KeyMailLanguage: string(mailing.English)},
	}, rbac.PermissionUsersManage)

	rec := f.do(http.MethodPost, "/users",
		`{"login":"a.rusu","full_name":"Ana Rusu","email":"ana@usm.md","send_by_email":true}`)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := f.repo.ByLogin(context.Background(), "a.rusu"); err == nil {
		t.Error("the account was created without its message")
	}
}

func TestResettingAgainLeavesOnlyTheNewestMessageQueued(t *testing.T) {
	f, queue := newEmailingFixture(t, true)
	target := f.repo.Add(users.User{Login: "petrov", FullName: "Pyotr", Email: "petrov@usm.md"})
	path := "/users/" + target.ID.String() + "/password-reset"

	f.do(http.MethodPost, path, `{"send_by_email":true}`)
	rec := f.do(http.MethodPost, path, `{"send_by_email":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	assertQueuedTo(t, queue, "petrov@usm.md", decodeJSON[emailed](t, rec).OneTimePassword)
}
