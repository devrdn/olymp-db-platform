package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
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
	PasswordSignup     bool   `json:"password_signup"`
	PasswordReset      bool   `json:"password_reset"`
	Microsoft          bool   `json:"microsoft"`
	TrustedEmailDomain string `json:"trusted_email_domain"`
}

// NewAuthMethodsHandler reads the answers once: each is decided at start-up,
// as the routes it describes are.
func NewAuthMethodsHandler(signups, recoveries offerer, microsoft bool, trustedEmailDomain string) *AuthMethodsHandler {
	return &AuthMethodsHandler{body: authMethodsResponse{
		PasswordSignup:     signups.Offered(),
		PasswordReset:      recoveries.Offered(),
		Microsoft:          microsoft,
		TrustedEmailDomain: trustedEmailDomain,
	}}
}

// Mount registers GET /auth/methods, which needs no session. Not cached, so
// a changed configuration shows at the next page load.
func (h *AuthMethodsHandler) Mount(r chi.Router) {
	r.With(noStore).Get("/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, h.body)
	})
}
