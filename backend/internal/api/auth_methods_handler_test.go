package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/api"
)

type offered bool

func (o offered) Offered() bool { return bool(o) }

func TestAuthMethodsReportsTheInstallation(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		signup, reset, microsoft bool
		domain                   string
	}{
		{"nothing but passwords", false, false, false, ""},
		{"everything", true, true, true, "example.edu"},
		{"Microsoft only", false, false, true, ""},
		{"reset without sign-up", false, true, false, "example.edu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := chi.NewRouter()
			api.NewAuthMethodsHandler(offered(tc.signup), offered(tc.reset), tc.microsoft, tc.domain).Mount(router)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/methods", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /auth/methods = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode %s: %v", rec.Body.String(), err)
			}
			want := map[string]any{
				"password_signup": tc.signup, "password_reset": tc.reset,
				"microsoft": tc.microsoft, "trusted_email_domain": tc.domain,
			}
			if len(body) != len(want) {
				t.Errorf("body %v, want exactly %v", body, want)
			}
			for key, value := range want {
				if body[key] != value {
					t.Errorf("%s = %#v, want %#v", key, body[key], value)
				}
			}
		})
	}
}
