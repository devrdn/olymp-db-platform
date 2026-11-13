package connector_test

import (
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/connector"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/microsoft/microsofttest"
)

// The key set is exercised through the Microsoft adapter and its fake, the
// one provider whose tokens it verifies today.
const (
	clientID     = "11111111-2222-4333-8444-555555555555"
	clientSecret = "a-test-client-secret"
	homeTenant   = "0a0a0a0a-1b1b-4c2c-8d3d-4e4e4e4e4e4e"
	objectID     = "abababab-cdcd-4efe-8a0a-121212121212"
	redirectURL  = "https://contest.example.edu/api/v1/auth/microsoft/callback"
)

func newClient(t *testing.T, p *microsofttest.Provider, edit func(*microsoft.Config)) *microsoft.Client {
	t.Helper()
	cfg := microsoft.Config{
		ClientID: clientID, ClientSecret: clientSecret,
		Tenant: microsoft.TenantCommon, HomeTenant: homeTenant,
		RedirectURL: redirectURL, BaseURL: p.URL,
	}
	if edit != nil {
		edit(&cfg)
	}
	c, err := microsoft.New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return c
}

func exchange(t *testing.T, c *microsoft.Client, p *microsofttest.Provider, claims map[string]any) (federation.Claims, error) {
	t.Helper()
	return exchangeToken(t, c, p, p.Sign(t, claims))
}

func exchangeToken(t *testing.T, c *microsoft.Client, p *microsofttest.Provider, idToken string) (federation.Claims, error) {
	t.Helper()
	p.Issue("the-code", idToken, microsofttest.Challenge(strings.Repeat("v", 43)))
	return c.Exchange(t.Context(), "the-code", connector.State{Nonce: "the-nonce", Verifier: strings.Repeat("v", 43)})
}
