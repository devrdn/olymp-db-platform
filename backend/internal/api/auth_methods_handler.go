package api

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// offerer is a way in that may be switched off (signup.Service,
// recovery.Service).
type offerer interface {
	Offered() bool
}

// AuthMethodsHandler tells the interface which ways in this installation
// offers and which email domain is its own, so no installation's values are
// written into the interface.
type AuthMethodsHandler struct {
	body authMethodsResponse
}

type authMethodsResponse struct {
	PasswordSignup bool `json:"password_signup"`
	PasswordReset  bool `json:"password_reset"`
	// Microsoft is kept for interfaces that predate Providers.
	Microsoft          bool           `json:"microsoft"`
	Providers          []providerInfo `json:"providers"`
	TrustedEmailDomain string         `json:"trusted_email_domain"`
}

type providerInfo struct {
	ID string `json:"id"`
}

// AuthMethods is what the methods handler is built from. Providers are the
// configured provider ids, in the order they are offered.
type AuthMethods struct {
	Signups, Recoveries offerer
	Providers           []string
	TrustedEmailDomain  string
}

// NewAuthMethodsHandler reads the answers once: each is decided at start-up,
// as the routes it describes are.
func NewAuthMethodsHandler(cfg AuthMethods) *AuthMethodsHandler {
	listed := make([]providerInfo, 0, len(cfg.Providers))
	for _, id := range cfg.Providers {
		listed = append(listed, providerInfo{ID: id})
	}
	return &AuthMethodsHandler{body: authMethodsResponse{
		PasswordSignup:     cfg.Signups.Offered(),
		PasswordReset:      cfg.Recoveries.Offered(),
		Microsoft:          slices.Contains(cfg.Providers, users.ProviderMicrosoft),
		Providers:          listed,
		TrustedEmailDomain: cfg.TrustedEmailDomain,
	}}
}

// Mount registers GET /auth/methods, which needs no session. Not cached, so
// a changed configuration shows at the next page load.
func (h *AuthMethodsHandler) Mount(r chi.Router) {
	r.With(noStore).Get("/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, h.body)
	})
}
