package api

import (
	"context"
	"errors"
	"io"
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
	Images(ctx context.Context) (map[string]string, error)
	Image(ctx context.Context, kind string) (settings.Image, error)
	SaveImage(ctx context.Context, actorID uuid.UUID, kind string, data []byte) (settings.Image, error)
	RemoveImage(ctx context.Context, actorID uuid.UUID, kind string) error
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

// Mount registers the routes under /settings. The read is open because the
// sign-in screen shows the installation's name and logo, so it returns an
// allow-list, not the table. The write needs settings.manage, held only by
// administrators.
func (h *SettingsHandler) Mount(r chi.Router) {
	r.Route("/settings", func(r chi.Router) {
		// Outside authentication; the domain's Public catalogue decides what it
		// returns.
		r.Get("/", h.public)

		// The pictures are public for the same reason.
		r.Get("/images/{kind}", h.image)

		r.Group(func(r chi.Router) {
			r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionSettingsManage))
			r.Get("/all", h.all)
			r.Put("/", h.save)
			r.Put("/images/{kind}", h.uploadImage)
			r.Delete("/images/{kind}", h.removeImage)
		})
	})
}

type settingsResponse struct {
	Values settings.Values `json:"values"`
	// Images maps a slot to the hash its URL carries, so a page can link a
	// picture without fetching it.
	Images map[string]string `json:"images"`
}

// maxUploadBytes bounds the request body before any of it is held. Larger than
// the domain limit, so an oversized picture still gets the domain's actionable
// message.
const maxUploadBytes = 2 << 20

func (h *SettingsHandler) public(w http.ResponseWriter, r *http.Request) {
	values, err := h.settings.Public(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the public settings", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, settingsResponse{Values: values, Images: h.presentImages(r)})
}

// presentImages reports which slots hold a picture and never fails the request:
// a page that cannot learn about the logo should still get the name.
func (h *SettingsHandler) presentImages(r *http.Request) map[string]string {
	present, err := h.settings.Images(r.Context())
	if err != nil {
		h.log.WarnContext(r.Context(), "could not list the installation images", "error", err)
		return map[string]string{}
	}
	return present
}

// image serves one of the installation's pictures, openly, since the sign-in
// screen shows them:
//
//   - `nosniff`, so the browser uses the type detected when the bytes were
//     stored.
//   - `Content-Disposition: attachment`, so opening the address downloads the
//     file instead of rendering it on this origin; an `<img>` is unaffected.
//     The accepted raster formats cannot carry script, so this is a second
//     lock.
//   - immutable caching, safe because the address carries the hash.
func (h *SettingsHandler) image(w http.ResponseWriter, r *http.Request) {
	img, err := h.settings.Image(r.Context(), chi.URLParam(r, "kind"))
	switch {
	case err == nil:
	case errors.Is(err, settings.ErrImageNotFound), errors.Is(err, settings.ErrUnknownImageKind):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "No such image")
		return
	default:
		h.log.ErrorContext(r.Context(), "could not read an installation image", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	header := w.Header()
	header.Set("Content-Type", img.ContentType)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Disposition", "attachment")
	header.Set("Cache-Control", "public, max-age=31536000, immutable")
	header.Set("ETag", `"`+img.SHA256+`"`)

	if match := r.Header.Get("If-None-Match"); match == `"`+img.SHA256+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.Bytes)
}

func (h *SettingsHandler) uploadImage(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	// Bounded before anything is held: the declared length is only the sender's
	// claim.
	body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	data, err := io.ReadAll(body)
	if err != nil {
		// Over the reader's limit; to the uploader it is the same refusal as
		// the domain's.
		httpx.Error(w, r, http.StatusBadRequest, codeImageTooLarge, "The upload is too large to read")
		return
	}

	saved, err := h.settings.SaveImage(r.Context(), identity.UserID, chi.URLParam(r, "kind"), data)
	switch {
	case err == nil:
	case errors.Is(err, settings.ErrUnknownImageKind):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "No such image")
		return
	case errors.Is(err, settings.ErrImageTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeImageTooLarge, err.Error())
		return
	case errors.Is(err, settings.ErrNotAnImage):
		httpx.Error(w, r, http.StatusBadRequest, codeImageNotAccepted, err.Error())
		return
	default:
		h.log.ErrorContext(r.Context(), "could not save an installation image", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"kind": saved.Kind, "sha256": saved.SHA256,
		"width": saved.Width, "height": saved.Height,
	})
}

func (h *SettingsHandler) removeImage(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	switch err := h.settings.RemoveImage(r.Context(), identity.UserID, chi.URLParam(r, "kind")); {
	case err == nil:
	case errors.Is(err, settings.ErrUnknownImageKind):
		httpx.Error(w, r, http.StatusNotFound, codeNotFound, "No such image")
		return
	default:
		h.log.ErrorContext(r.Context(), "could not remove an installation image", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.NoContent(w, r)
}

func (h *SettingsHandler) all(w http.ResponseWriter, r *http.Request) {
	values, err := h.settings.All(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the settings", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}
	httpx.JSON(w, r, http.StatusOK, settingsResponse{Values: values, Images: h.presentImages(r)})
}

type saveSettingsRequest struct {
	Values settings.Values `json:"values"`
}

func (h *SettingsHandler) save(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())

	var req saveSettingsRequest
	if !decodeBody(w, r, &req) {
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
