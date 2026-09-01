package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SettingsStore is the slice of the settings service these endpoints need.
type SettingsStore interface {
	All(ctx context.Context) (settings.Values, error)
	Public(ctx context.Context) (settings.Values, error)
	Save(ctx context.Context, actorID uuid.UUID, values settings.Values) error
}

// SettingsHandler serves what the installation calls itself.
type SettingsHandler struct {
	settings SettingsStore
	mw       *auth.Middleware
	log      *slog.Logger
}

// NewSettingsHandler assembles the settings endpoints.
func NewSettingsHandler(store SettingsStore, mw *auth.Middleware, log *slog.Logger) *SettingsHandler {
	return &SettingsHandler{settings: store, mw: mw, log: log}
}

// Mount registers the routes under /settings.
//
// Two doors of different widths, and the difference is the point. The sign-in
// screen carries the installation's name and logo and is seen before anybody
// signs in, so the read has to be open — and an open read must therefore
// return an allow-list rather than the table. The write is behind
// `settings.manage`, which only administrators hold: naming the university is
// not something an organizer does from inside a contest they happen to run.
func (h *SettingsHandler) Mount(r chi.Router) {
	r.Route("/settings", func(r chi.Router) {
		// Deliberately outside the authentication middleware. What it returns
		// is decided by the domain's Public catalogue, never by the caller.
		r.Get("/", h.public)

		r.Group(func(r chi.Router) {
			r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionSettingsManage))
			r.Get("/all", h.all)
			r.Put("/", h.save)
		})
	})
}

type settingsResponse struct {
	Values settings.Values `json:"values"`
}

// public serves what an unauthenticated page may read.
func (h *SettingsHandler) public(w http.ResponseWriter, r *http.Request) {
	values, err := h.settings.Public(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the public settings", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, settingsResponse{Values: values})
}

// all serves every setting, for the screen that edits them.
func (h *SettingsHandler) all(w http.ResponseWriter, r *http.Request) {
	values, err := h.settings.All(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the settings", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, settingsResponse{Values: values})
}

type saveSettingsRequest struct {
	Values settings.Values `json:"values"`
}

func (h *SettingsHandler) save(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	var req saveSettingsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	switch err := h.settings.Save(r.Context(), identity.UserID, req.Values); {
	case err == nil:
	case errors.Is(err, settings.ErrUnknownKey), errors.Is(err, settings.ErrInvalid):
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	default:
		h.log.ErrorContext(r.Context(), "could not save the settings", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	values, err := h.settings.All(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the settings back", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, settingsResponse{Values: values})
}
