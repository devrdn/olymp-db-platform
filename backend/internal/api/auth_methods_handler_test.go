package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
)

type offered bool

func (o offered) Offered() bool { return bool(o) }

func TestAuthMethodsListsTheConfiguredProvidersInOrder(t *testing.T) {
	for _, tc := range []struct {
		name          string
		signup, reset bool
		providers     []string
		microsoft     bool
		domain        string
		listed        []any
	}{
		{"nothing but passwords", false, false, nil, false, "", []any{}},
		{"Microsoft and GitHub", true, true, []string{"microsoft", "github"}, true, "example.edu",
			[]any{map[string]any{"id": "microsoft"}, map[string]any{"id": "github"}}},
		{"Google only", false, false, []string{"google"}, false, "", []any{map[string]any{"id": "google"}}},
		{"reset without sign-up", false, true, nil, false, "example.edu", []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			api.NewAuthMethodsHandler(api.AuthMethods{
				Signups: offered(tc.signup), Recoveries: offered(tc.reset),
				Providers: tc.providers, TrustedEmailDomain: tc.domain, Switches: &fakeSwitches{},
			}).Mount(router)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/methods", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /auth/methods = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if len(tc.providers) == 0 && !strings.Contains(rec.Body.String(), `"providers":[]`) {
				t.Errorf("body %s, want \"providers\":[]", rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			want := map[string]any{
				"password_signup": tc.signup, "password_reset": tc.reset,
				"microsoft": tc.microsoft, "providers": tc.listed, "trusted_email_domain": tc.domain,
			}
			if len(body) != len(want) {
				t.Errorf("body %v, want exactly %v", body, want)
			}
			for key, value := range want {
				if !reflect.DeepEqual(body[key], value) {
					t.Errorf("%s = %#v, want %#v", key, body[key], value)
				}
			}
		})
	}
}

func getMethods(t *testing.T, router http.Handler) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/methods", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestAuthMethodsHidesASwitchedOffProvider(t *testing.T) {
	router := chi.NewRouter()
	api.NewAuthMethodsHandler(api.AuthMethods{
		Signups: offered(true), Recoveries: offered(true),
		Providers: []string{"microsoft", "google"},
		Switches:  &fakeSwitches{off: map[string]bool{"microsoft": true}},
	}).Mount(router)

	_, body := getMethods(t, router)

	if want := []any{map[string]any{"id": "google"}}; !reflect.DeepEqual(body["providers"], want) {
		t.Errorf("providers = %#v, want %#v", body["providers"], want)
	}
	if body["microsoft"] != false {
		t.Errorf("microsoft = %v, want false while it is switched off", body["microsoft"])
	}
}

func TestAuthMethodsReadsTheSwitchesAtMostOnceInFiveSeconds(t *testing.T) {
	switches := &fakeSwitches{off: map[string]bool{}}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	router := chi.NewRouter()
	api.NewAuthMethodsHandler(api.AuthMethods{
		Signups: offered(false), Recoveries: offered(false),
		Providers: []string{"google"}, Switches: switches,
		Now: func() time.Time { return now },
	}).Mount(router)

	getMethods(t, router)
	getMethods(t, router)
	now = now.Add(4 * time.Second)
	getMethods(t, router)
	if switches.reads != 1 {
		t.Fatalf("%d reads within 4 s, want 1", switches.reads)
	}

	switches.off["google"] = true
	now = now.Add(2 * time.Second)
	_, body := getMethods(t, router)

	if switches.reads != 2 {
		t.Errorf("%d reads after 6 s, want 2", switches.reads)
	}
	if !reflect.DeepEqual(body["providers"], []any{}) {
		t.Errorf("providers = %#v, want none once the cache expired", body["providers"])
	}
}

func TestAnUnreadableSwitchHidesTheButtons(t *testing.T) {
	switches := &fakeSwitches{err: errors.New("the database is down")}
	logs := &logBuffer{}
	router := chi.NewRouter()
	api.NewAuthMethodsHandler(api.AuthMethods{
		Signups: offered(true), Recoveries: offered(false), TrustedEmailDomain: "example.edu",
		Providers: []string{"microsoft"}, Switches: switches, Log: logging.New("warn", logs),
	}).Mount(router)

	status, body := getMethods(t, router)
	getMethods(t, router)

	if status != http.StatusOK {
		t.Fatalf("status %d, want 200", status)
	}
	if !reflect.DeepEqual(body["providers"], []any{}) || body["microsoft"] != false {
		t.Errorf("providers %#v, microsoft %v, want none", body["providers"], body["microsoft"])
	}
	if body["password_signup"] != true || body["password_reset"] != false || body["trusted_email_domain"] != "example.edu" {
		t.Errorf("password fields changed: %v", body)
	}
	if switches.reads != 2 {
		t.Errorf("%d reads for two requests, want 2: a failed read is not cached", switches.reads)
	}
	if !logs.loggedError("could not read the sign-in switches") {
		t.Error("the read error was not logged")
	}
}
