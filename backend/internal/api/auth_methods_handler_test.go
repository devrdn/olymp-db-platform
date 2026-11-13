package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
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
				Providers: tc.providers, TrustedEmailDomain: tc.domain,
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
