package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// userIDParam names the account in the URL.
const userIDParam = "userID"

// UsersHandler serves the account management endpoints. Every route requires the
// installation-wide users.manage permission: these operations are not scoped
// to a contest, and running one must never grant power over accounts.
type UsersHandler struct {
	service *users.Service
	mw      *auth.Middleware
	log     *slog.Logger
}

// NewUsersHandler assembles the account endpoints.
func NewUsersHandler(service *users.Service, mw *auth.Middleware, log *slog.Logger) *UsersHandler {
	return &UsersHandler{service: service, mw: mw, log: log}
}

// Mount registers the routes under /users.
func (h *UsersHandler) Mount(r chi.Router) {
	r.Route("/users", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))

		r.Get("/", h.list)
		r.Post("/", h.create)

		r.Route("/{"+userIDParam+"}", func(r chi.Router) {
			r.Get("/", h.byID)
			r.Patch("/", h.updateProfile)
			r.Post("/block", h.block)
			r.Post("/unblock", h.unblock)
			r.Post("/password-reset", h.resetPassword)
			r.Put("/roles", h.replaceRoles)
		})
	})
}

// UserResponse is the account as the API describes it.
//
// A dedicated type rather than the domain object: serialising users.User directly
// would publish PasswordHash the first time somebody forgets a json tag.
type UserResponse struct {
	ID       string   `json:"id"`
	Login    string   `json:"login"`
	Email    string   `json:"email,omitempty"`
	FullName string   `json:"full_name"`
	Status   string   `json:"status"`
	Roles    []string `json:"roles"`
	// MustChangePassword tells the interface to send the user to the password
	// form after signing in.
	MustChangePassword bool   `json:"must_change_password"`
	LastLoginAt        string `json:"last_login_at,omitempty"`
	CreatedAt          string `json:"created_at"`
}

func toUserResponse(u users.User) UserResponse {
	out := UserResponse{
		ID:                 u.ID.String(),
		Login:              u.Login,
		Email:              u.Email,
		FullName:           u.FullName,
		Status:             u.Status,
		Roles:              u.Roles,
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if u.LastLoginAt != nil {
		out.LastLoginAt = u.LastLoginAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

type createRequest struct {
	Login    string   `json:"login"`
	Email    string   `json:"email"`
	FullName string   `json:"full_name"`
	Roles    []string `json:"roles"`
}

type createResponse struct {
	User UserResponse `json:"user"`
	// OneTimePassword is shown once, here. It is not stored in clear and
	// cannot be fetched later; a lost one is reset.
	OneTimePassword string `json:"one_time_password"`
}

func (h *UsersHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	created, err := h.service.Create(r.Context(), users.CreateCommand{
		ActorID:  identity.UserID,
		Login:    req.Login,
		Email:    req.Email,
		FullName: req.FullName,
		Roles:    req.Roles,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusCreated, createResponse{
		User:            toUserResponse(created.User),
		OneTimePassword: created.OneTimePassword,
	})
}

type listResponse struct {
	Items []UserResponse `json:"items"`
	Total int            `json:"total"`
}

func (h *UsersHandler) list(w http.ResponseWriter, r *http.Request) {
	filter := users.Filter{
		Query:  r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Limit:  intParam(r, "limit"),
		Offset: intParam(r, "offset"),
	}

	found, total, err := h.service.List(r.Context(), filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]UserResponse, 0, len(found))
	for _, u := range found {
		items = append(items, toUserResponse(u))
	}
	httpx.JSON(w, r, http.StatusOK, listResponse{Items: items, Total: total})
}

func (h *UsersHandler) byID(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	user, err := h.service.ByID(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toUserResponse(user))
}

type updateProfileRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

func (h *UsersHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	var req updateProfileRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.UpdateProfile(r.Context(), identity.UserID, id, req.FullName, req.Email); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *UsersHandler) block(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Block(r.Context(), identity.UserID, id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *UsersHandler) unblock(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Unblock(r.Context(), identity.UserID, id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

type resetResponse struct {
	OneTimePassword string `json:"one_time_password"`
}

func (h *UsersHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	issued, err := h.service.ResetPassword(r.Context(), identity.UserID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, resetResponse{OneTimePassword: issued})
}

type rolesRequest struct {
	Roles []string `json:"roles"`
}

func (h *UsersHandler) replaceRoles(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	var req rolesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.ReplaceRoles(r.Context(), identity.UserID, id, req.Roles); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// accountID reads and validates the account in the URL.
func (h *UsersHandler) accountID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, userIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_user_id", "users.User identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// fail maps a service error onto a response. Anything unrecognised becomes a
// 500 with the detail kept in the log, never in the body.
func (h *UsersHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, users.ErrNotFound):
		httpx.Error(w, r, http.StatusNotFound, "not_found", "users.User not found")
	case errors.Is(err, users.ErrLoginTaken):
		httpx.Error(w, r, http.StatusConflict, "login_taken", "This login is already in use")
	case errors.Is(err, users.ErrCannotActOnSelf):
		httpx.Error(w, r, http.StatusBadRequest, "cannot_act_on_self",
			"This operation cannot be performed on your own account")
	case errors.Is(err, users.ErrWeakPassword), errors.Is(err, users.ErrSamePassword):
		httpx.Error(w, r, http.StatusBadRequest, "invalid_password", err.Error())
	default:
		h.log.ErrorContext(r.Context(), "account operation failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
	}
}

func intParam(r *http.Request, name string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return value
}
