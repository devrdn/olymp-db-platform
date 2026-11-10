package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
)

func TestSignInKeepsTheSessionCookieWhateverTheAPIsTLSSetting(t *testing.T) {
	for name, secure := range map[string]bool{"secure": true, "plain HTTP": false} {
		t.Run(name, func(t *testing.T) {
			cookies := auth.NewCookieWriter(secure)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				cookies.Set(w, "session-token", time.Hour)
			}))
			t.Cleanup(api.Close)
			p := newParticipant(1, participantRecord{Login: "p001"}, 1)

			if err := p.signIn(t.Context(), api.URL, "secret"); err != nil {
				t.Fatalf("signIn() = %v", err)
			}
			if p.session.Name != cookies.SessionName() || p.session.Value != "session-token" {
				t.Errorf("session = %s=%s, want %s=session-token", p.session.Name, p.session.Value, cookies.SessionName())
			}
		})
	}
}
