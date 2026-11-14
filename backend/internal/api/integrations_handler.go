package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/httpx"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// IntegrationSwitches reads and writes the administrator's on/off switch of
// each sign-in provider (settings.Service).
type IntegrationSwitches interface {
	DisabledSignInProviders(ctx context.Context) (map[string]bool, error)
	SetSignInProviderEnabled(ctx context.Context, actorID uuid.UUID, provider string, enabled bool) error
}

// Integrations is what the integrations handler is built from. PublicURL is
// the installation's address, empty when it is not set.
type Integrations struct {
	Providers  []SignInProvider
	PublicURL  string
	Switches   IntegrationSwitches
	Middleware *auth.Middleware
	Log        *slog.Logger
}

// IntegrationsHandler shows an administrator the sign-in providers and
// switches the configured ones on or off. It never returns a client ID or
// secret: the environment holds those, and the page only names the variables.
type IntegrationsHandler struct {
	cfg Integrations
}

// NewIntegrationsHandler assembles the integrations endpoints.
func NewIntegrationsHandler(cfg Integrations) *IntegrationsHandler {
	return &IntegrationsHandler{cfg: cfg}
}

// Mount registers the routes under /integrations, for settings.manage.
func (h *IntegrationsHandler) Mount(r chi.Router) {
	r.Route("/integrations", func(r chi.Router) {
		r.Use(h.cfg.Middleware.Authenticate, h.cfg.Middleware.RequirePermission(rbac.PermissionSettingsManage))
		r.With(noStore).Get("/", h.list)
		r.Put("/{provider}", h.switchProvider)
	})
}

type integrationView struct {
	ID          string   `json:"id"`
	Configured  bool     `json:"configured"`
	Enabled     bool     `json:"enabled"`
	RedirectURI string   `json:"redirect_uri"`
	Variables   []string `json:"variables"`
}

type integrationsResponse struct {
	Providers []integrationView `json:"providers"`
}

type switchRequest struct {
	Enabled *bool `json:"enabled"`
}

func (h *IntegrationsHandler) list(w http.ResponseWriter, r *http.Request) {
	off, err := h.cfg.Switches.DisabledSignInProviders(r.Context())
	if err != nil {
		h.fail(w, r, "could not read the sign-in switches", err)
		return
	}
	views := make([]integrationView, 0, len(h.cfg.Providers))
	for _, p := range h.cfg.Providers {
		redirect := ""
		if h.cfg.PublicURL != "" {
			redirect = h.cfg.PublicURL + CallbackPath(p.ID)
		}
		variables := p.Variables
		if variables == nil {
			variables = []string{}
		}
		views = append(views, integrationView{
			ID: p.ID, Configured: p.Connector != nil, Enabled: !off[p.ID],
			RedirectURI: redirect, Variables: variables,
		})
	}
	httpx.JSON(w, r, http.StatusOK, integrationsResponse{Providers: views})
}

func (h *IntegrationsHandler) switchProvider(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	if !h.configured(chi.URLParam(r, "provider")) {
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "No such sign-in provider")
		return
	}

	var req switchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, "enabled is required")
		return
	}

	if err := h.cfg.Switches.SetSignInProviderEnabled(r.Context(), identity.UserID, chi.URLParam(r, "provider"), *req.Enabled); err != nil {
		h.fail(w, r, "could not switch the sign-in provider", err)
		return
	}
	httpx.NoContent(w, r)
}

// configured reports whether id names a provider with a connector; one
// without has nothing to switch.
func (h *IntegrationsHandler) configured(id string) bool {
	for _, p := range h.cfg.Providers {
		if p.ID == id {
			return p.Connector != nil
		}
	}
	return false
}

func (h *IntegrationsHandler) fail(w http.ResponseWriter, r *http.Request, message string, err error) {
	h.cfg.Log.ErrorContext(r.Context(), message, "error", err)
	httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}
