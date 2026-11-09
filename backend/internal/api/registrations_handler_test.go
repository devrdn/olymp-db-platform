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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/cache"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/devrdn/db-contest/backend/internal/registrations/registrationstest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/devrdn/db-contest/backend/internal/users/userstest"
)

type registrationsFixture struct {
	*registrationstest.Fixture
	router http.Handler
	cookie *http.Cookie
}

// newRegistrationsFixture mounts the routes with the public form open, with
// the signed-in account holding permissions.
func newRegistrationsFixture(t *testing.T, permissions ...string) *registrationsFixture {
	t.Helper()
	return mountRegistrations(t, true, permissions...)
}

func mountRegistrations(t *testing.T, formOpen bool, permissions ...string) *registrationsFixture {
	t.Helper()
	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("staff", permissions...)
	staff := repo.Add(users.User{Login: "staff", FullName: "Staff", Status: users.StatusActive, Roles: []string{"staff"}})
	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: staff.ID, Login: staff.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}
	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	f := &registrationsFixture{Fixture: registrationstest.NewFixture(nil)}
	router := chi.NewRouter()
	handler := api.NewRegistrationsHandler(f.Service, auth.NewLimiter(c), mw, log)
	if formOpen {
		handler = handler.WithPublicForm()
	}
	handler.Mount(router)
	f.router = router
	f.cookie = &http.Cookie{Name: auth.SessionCookieName, Value: token}
	return f
}

func (f *registrationsFixture) register(from, first, last, email string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"first_name": first, "last_name": last, "email": email})
	req := httptest.NewRequest(http.MethodPost, "/public/registrations", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = from + ":4000"
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *registrationsFixture) staff(method, path, body string) *httptest.ResponseRecorder {
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

func jsonFields(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not a JSON object: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestThePublicFormIsMountedOnlyWhenOpenedAndTheStaffRoutesAlways(t *testing.T) {
	for _, c := range []struct {
		name     string
		open     bool
		wantForm int
	}{
		{"closed", false, http.StatusNotFound},
		{"open", true, http.StatusCreated},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := mountRegistrations(t, c.open, rbac.PermissionUsersManage)

			if rec := f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com"); rec.Code != c.wantForm {
				t.Errorf("POST /public/registrations = %d, want %d", rec.Code, c.wantForm)
			}
			if rec := f.staff(http.MethodGet, "/registrations", ""); rec.Code != http.StatusOK {
				t.Errorf("GET /registrations = %d, want 200", rec.Code)
			}
			rec := f.staff(http.MethodPut, "/registrations/"+uuid.NewString()+"/check-in", `{"checked_in":true}`)
			if rec.Code != http.StatusNotFound || errorCode(t, rec) != "registration_not_found" {
				t.Errorf("PUT check-in = %d %q, want the handler's 404 registration_not_found", rec.Code, errorCode(t, rec))
			}
		})
	}
}

func TestAFirstRegistrationAnswersTheCodeAndTheAccount(t *testing.T) {
	f := newRegistrationsFixture(t)

	rec := f.register("192.0.2.1", "Ana", "Popescu", "Ana@Example.com")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the answer carries a password", got)
	}
	body := jsonFields(t, rec)
	code := f.Store.Rows()[0].Code
	want := map[string]any{
		"status": "created", "code": code, "first_name": "Ana", "last_name": "Popescu",
		"email": "Ana@Example.com", "account": "created", "login": "ana@example.com",
		"one_time_password": "one-time-ana@example.com", "email_sent": true,
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %v, want %v", key, body[key], value)
		}
	}
	if len(body) != len(want) {
		t.Errorf("body = %v, want exactly %v", body, want)
	}
}

func TestARegistrationWithAnExistingAccountShowsNoPassword(t *testing.T) {
	f := newRegistrationsFixture(t)
	f.Accounts.Taken = map[string]bool{"ana@example.com": true}

	rec := f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	body := jsonFields(t, rec)
	if body["account"] != "existing" || body["code"] == nil {
		t.Errorf("body = %v, want the code with an existing account", body)
	}
	for _, absent := range []string{"login", "one_time_password"} {
		if _, ok := body[absent]; ok {
			t.Errorf("body carries %q for an account it did not make: %v", absent, body)
		}
	}
}

// Anyone who knows an address could otherwise read its code off the page.
func TestARepeatRegistrationShowsNoCode(t *testing.T) {
	f := newRegistrationsFixture(t)
	f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")

	rec := f.register("192.0.2.2", "Someone", "Else", "ANA@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := jsonFields(t, rec)
	want := map[string]any{"status": "existing", "email": "ANA@example.com", "email_sent": false}
	if len(body) != len(want) {
		t.Errorf("body = %v, want exactly %v", body, want)
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %v, want %v (within the cooldown nothing is resent)", key, body[key], value)
		}
	}
}

func TestARegistrationAnswersItsRefusals(t *testing.T) {
	f := newRegistrationsFixture(t)

	cases := []struct {
		name, first, email string
		status             int
		code               string
	}{
		{"empty name", " ", "ana@example.com", http.StatusBadRequest, "registration_invalid_name"},
		{"display name", "Ana", "Ana <ana@example.com>", http.StatusBadRequest, "registration_invalid_email"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := f.register("192.0.2.1", c.first, "Popescu", c.email)
			if rec.Code != c.status || errorCode(t, rec) != c.code {
				t.Errorf("= %d %q, want %d %q", rec.Code, errorCode(t, rec), c.status, c.code)
			}
		})
	}

	t.Run("full", func(t *testing.T) {
		f.Store.Fill(registrations.Capacity)
		rec := f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "registration_full" {
			t.Errorf("= %d %q, want 409 registration_full", rec.Code, errorCode(t, rec))
		}
	})
}

func TestRegistrationsAreLimitedPerAddressBeforeAnyStorage(t *testing.T) {
	f := newRegistrationsFixture(t)
	for i := range api.RegistrationsPerAddress {
		if rec := f.register("192.0.2.1", "Ana", "Popescu", fmt.Sprintf("ana%d@example.com", i)); rec.Code != http.StatusCreated {
			t.Fatalf("registration %d = %d, want 201", i, rec.Code)
		}
	}
	reads := f.Store.Writes

	rec := f.register("192.0.2.1", "Ana", "Popescu", "late@example.com")

	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
		t.Errorf("= %d %q, want 429 too_many_attempts", rec.Code, errorCode(t, rec))
	}
	if f.Store.Writes != reads {
		t.Error("a refused registration reached the store")
	}
	if rec := f.register("192.0.2.9", "Ana", "Popescu", "late@example.com"); rec.Code != http.StatusCreated {
		t.Errorf("another address = %d, want its own budget", rec.Code)
	}
}

func TestRegistrationsAreLimitedPerEmailWhateverItsCase(t *testing.T) {
	f := newRegistrationsFixture(t)
	for i := range api.RegistrationsPerEmail {
		rec := f.register(fmt.Sprintf("192.0.2.%d", i+1), "Ana", "Popescu", "Ana@example.com")
		if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("attempt %d = %d, want it admitted", i, rec.Code)
		}
	}

	rec := f.register("192.0.2.100", "Ana", "Popescu", "ana@EXAMPLE.com")

	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
		t.Errorf("= %d %q, want 429 too_many_attempts", rec.Code, errorCode(t, rec))
	}
}

// CLAUDE.md rule 5: a caller refused for its address spends no email budget,
// so it cannot make counters for addresses it invents.
func TestTheAddressIsCheckedBeforeTheEmail(t *testing.T) {
	f := newRegistrationsFixture(t)
	for i := range api.RegistrationsPerAddress {
		f.register("192.0.2.1", "Ana", "Popescu", fmt.Sprintf("filler%d@example.com", i))
	}
	for range api.RegistrationsPerEmail {
		if rec := f.register("192.0.2.1", "Ana", "Popescu", "target@example.com"); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("over the address budget = %d, want 429", rec.Code)
		}
	}

	if rec := f.register("192.0.2.2", "Ana", "Popescu", "target@example.com"); rec.Code != http.StatusCreated {
		t.Errorf("the email from a fresh address = %d, want 201: refusals by address spent its budget", rec.Code)
	}
}

func TestTheRegistrationListNeedsTheUsersPermission(t *testing.T) {
	f := newRegistrationsFixture(t, rbac.PermissionReportsView)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/registrations", ""},
		{http.MethodPut, "/registrations/" + uuid.NewString() + "/check-in", `{"checked_in":true}`},
	} {
		if rec := f.staff(c.method, c.path, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", c.method, c.path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/registrations", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without a session = %d, want 401", rec.Code)
	}
}

type listedRegistration struct {
	ID          string  `json:"id"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	Email       string  `json:"email"`
	Code        string  `json:"code"`
	UserID      *string `json:"user_id"`
	CreatedAt   string  `json:"created_at"`
	CheckedInAt *string `json:"checked_in_at"`
}

func TestStaffListAndCheckInRegistrations(t *testing.T) {
	f := newRegistrationsFixture(t, rbac.PermissionUsersManage)
	f.Accounts.Taken = map[string]bool{"bob@example.com": true}
	f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")
	f.register("192.0.2.1", "Bob", "Ionescu", "bob@example.com")
	ana := f.Store.Rows()[0]

	rec := f.staff(http.MethodPut, "/registrations/"+ana.ID.String()+"/check-in", `{"checked_in":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("check-in = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var checked listedRegistration
	if err := json.Unmarshal(rec.Body.Bytes(), &checked); err != nil {
		t.Fatalf("body: %v", err)
	}
	if checked.ID != ana.ID.String() || checked.CheckedInAt == nil || checked.Code != ana.Code {
		t.Errorf("check-in answered %+v, want Ana arrived", checked)
	}

	rec = f.staff(http.MethodGet, "/registrations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var list struct {
		Registrations []listedRegistration `json:"registrations"`
		Total         int                  `json:"total"`
		CheckedIn     int                  `json:"checked_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("body: %v", err)
	}
	if list.Total != 2 || list.CheckedIn != 1 || len(list.Registrations) != 2 {
		t.Fatalf("list = %+v, want two with one arrived", list)
	}
	bob := list.Registrations[0]
	if bob.FirstName != "Bob" || bob.UserID != nil || bob.CheckedInAt != nil {
		t.Errorf("newest = %+v, want Bob, with no account of ours and not arrived", bob)
	}
	if _, err := time.Parse(time.RFC3339, bob.CreatedAt); err != nil {
		t.Errorf("created_at %q is not RFC 3339", bob.CreatedAt)
	}
	if a := list.Registrations[1]; a.UserID == nil || a.Email != "ana@example.com" {
		t.Errorf("oldest = %+v, want Ana with her account", a)
	}
}

func TestCheckInAnswersItsRefusals(t *testing.T) {
	f := newRegistrationsFixture(t, rbac.PermissionUsersManage)
	f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")
	ana := f.Store.Rows()[0]

	if rec := f.staff(http.MethodPut, "/registrations/"+uuid.NewString()+"/check-in", `{"checked_in":true}`); rec.Code != http.StatusNotFound || errorCode(t, rec) != "registration_not_found" {
		t.Errorf("unknown id = %d %q, want 404 registration_not_found", rec.Code, errorCode(t, rec))
	}
	if rec := f.staff(http.MethodPut, "/registrations/not-a-uuid/check-in", `{"checked_in":true}`); rec.Code != http.StatusNotFound || errorCode(t, rec) != "registration_not_found" {
		t.Errorf("malformed id = %d %q, want 404 registration_not_found", rec.Code, errorCode(t, rec))
	}
	if rec := f.staff(http.MethodPut, "/registrations/"+ana.ID.String()+"/check-in", `{}`); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_request" {
		t.Errorf("no checked_in = %d %q, want 400 invalid_request", rec.Code, errorCode(t, rec))
	}
}

func TestARegistrationWithNoHashingSlotIsBusyNotAFailure(t *testing.T) {
	f := newRegistrationsFixture(t)
	f.Accounts.Err = fmt.Errorf("create the account: %w", password.ErrBusy)

	rec := f.register("192.0.2.1", "Ana", "Popescu", "ana@example.com")

	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "sign_in_busy" {
		t.Errorf("= %d %q, want 503 sign_in_busy", rec.Code, errorCode(t, rec))
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a refusal for load")
	}
}
