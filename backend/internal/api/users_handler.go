package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const userIDParam = "userID"

// RoleCatalog lists the roles an account may hold.
type RoleCatalog interface {
	Roles(ctx context.Context) ([]users.Role, error)
}

// SignInUnlocker clears an account's sign-in throttling.
type SignInUnlocker interface {
	UnlockSignIn(ctx context.Context, actorID, userID uuid.UUID) error
}

// UsersHandler serves account management. Every route requires the
// installation-wide users.manage permission: no contest role may grant power
// over accounts.
type UsersHandler struct {
	service  *users.Service
	roles    RoleCatalog
	unlocker SignInUnlocker
	mw       *auth.Middleware
	log      *slog.Logger
}

// NewUsersHandler assembles the account endpoints.
func NewUsersHandler(service *users.Service, roles RoleCatalog, unlocker SignInUnlocker, mw *auth.Middleware, log *slog.Logger) *UsersHandler {
	return &UsersHandler{service: service, roles: roles, unlocker: unlocker, mw: mw, log: log}
}

// Mount registers the routes under /users, and the role catalogue beside them.
func (h *UsersHandler) Mount(r chi.Router) {
	// Beside /users rather than under it: it describes the installation, not an
	// account. Same permission, since it serves the same screen.
	r.Route("/roles", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))
		r.Get("/", h.listRoles)
	})

	r.Route("/users", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))

		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Post("/import", h.importRoster)

		// Registered before /{userID} so chi does not read "bulk" as an account
		// identifier.
		r.Route("/bulk", func(r chi.Router) {
			r.Post("/status", h.bulkStatus)
			r.Post("/roles", h.bulkRoles)
			r.Post("/password-reset", h.bulkResetPassword)
		})

		r.Route("/{"+userIDParam+"}", func(r chi.Router) {
			r.Get("/", h.byID)
			r.Patch("/", h.updateProfile)
			r.Post("/block", h.block)
			r.Post("/unblock", h.unblock)
			r.Post("/delete", h.deleteAccount)
			r.Post("/restore", h.restore)
			r.Post("/password-reset", h.resetPassword)
			r.Post("/sign-in/unlock", h.unlockSignIn)
			r.Put("/roles", h.replaceRoles)
		})
	})
}

// RoleResponse is one role as a person picks it from a list.
type RoleResponse struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type roleListResponse struct {
	Items []RoleResponse `json:"items"`
}

// listRoles publishes the roles an account may hold, so the interface never
// hard-codes them: roles are rows, and a client copy would go stale when one is
// added.
func (h *UsersHandler) listRoles(w http.ResponseWriter, r *http.Request) {
	catalogue, err := h.roles.Roles(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not read the role catalogue", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	items := make([]RoleResponse, 0, len(catalogue))
	for _, role := range catalogue {
		items = append(items, RoleResponse{Code: role.Code, Name: role.Name})
	}
	httpx.JSON(w, r, http.StatusOK, roleListResponse{Items: items})
}

// UserResponse is the account as the API describes it. A dedicated type, so a
// forgotten json tag never publishes PasswordHash.
type UserResponse struct {
	ID       string   `json:"id"`
	Login    string   `json:"login"`
	Email    string   `json:"email,omitempty"`
	FullName string   `json:"full_name"`
	Status   string   `json:"status"`
	Roles    []string `json:"roles"`
	// MustChangePassword sends the user to the password form after signing in.
	MustChangePassword bool   `json:"must_change_password"`
	LastLoginAt        string `json:"last_login_at,omitempty"`
	CreatedAt          string `json:"created_at"`
	// StatusReason, StatusChangedAt and StatusChangedBy say why, when and by
	// whom the status last changed; all empty for an account never blocked or
	// deleted.
	StatusReason    string `json:"status_reason,omitempty"`
	StatusChangedAt string `json:"status_changed_at,omitempty"`
	// StatusChangedBy is the actor's id.
	StatusChangedBy string `json:"status_changed_by,omitempty"`
	// StatusChangedByLogin is that actor's login, joined in by the repository;
	// empty exactly when StatusChangedBy is.
	StatusChangedByLogin string `json:"status_changed_by_login,omitempty"`
}

func toUserResponse(u users.User) UserResponse {
	out := UserResponse{
		ID:                   u.ID.String(),
		Login:                u.Login,
		Email:                u.Email,
		FullName:             u.FullName,
		Status:               u.Status,
		Roles:                u.Roles,
		MustChangePassword:   u.MustChangePassword,
		CreatedAt:            u.CreatedAt.UTC().Format(timeLayout),
		LastLoginAt:          formatTime(u.LastLoginAt),
		StatusChangedAt:      formatTime(u.StatusChangedAt),
		StatusReason:         u.StatusReason,
		StatusChangedByLogin: u.StatusChangedByLogin,
	}
	if u.StatusChangedBy != nil {
		out.StatusChangedBy = u.StatusChangedBy.String()
	}
	return out
}

// toIdentityResponse is what a signed-in account learns about itself. It clears
// the status metadata explicitly: an administrator's id and the moment they
// acted are not the owner's business.
func toIdentityResponse(u users.User) UserResponse {
	out := toUserResponse(u)
	out.StatusChangedAt = ""
	out.StatusChangedBy = ""
	out.StatusChangedByLogin = ""
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
	// OneTimePassword is shown once, here; it is not stored in clear and a lost
	// one is reset.
	OneTimePassword string `json:"one_time_password"`
}

func (h *UsersHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !decodeBody(w, r, &req) {
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

// importRequest is a whole group at once: accounts are created by an
// administrator (§7), and a group arrives as a list from the department.
type importRequest struct {
	Rows []importRow `json:"rows"`
	// Roles every created account receives, typically the student role.
	Roles []string `json:"roles"`
}

type importRow struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

// accountImportResponse reports the outcome row by row. Partial success: one
// duplicate must not reject the rest, and the person who pasted the list must
// see which line to fix. The one-time passwords appear here and nowhere else.
//
// If the hasher's load stops an import partway, it still answers 200 with what
// was done: NotImported names the rows never reached and Stopped says why, so
// they can be imported again. Any other failure is an error, not a result
// (CLAUDE.md rule 8).
type accountImportResponse struct {
	Created     []createResponse `json:"created"`
	Skipped     []skippedAccount `json:"skipped"`
	NotImported []string         `json:"not_imported"`
	Stopped     string           `json:"stopped,omitempty"`
}

type skippedAccount struct {
	Login  string `json:"login"`
	Reason string `json:"reason"`
}

func (h *UsersHandler) importRoster(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !decodeBody(w, r, &req) {
		return
	}

	rows := make([]users.ImportRow, 0, len(req.Rows))
	for _, row := range req.Rows {
		rows = append(rows, users.ImportRow{
			Login: row.Login, FullName: row.FullName, Email: row.Email,
		})
	}

	identity, _ := auth.IdentityFrom(r.Context())
	result, err := h.service.Import(r.Context(), users.ImportCommand{
		ActorID: identity.UserID,
		Rows:    rows,
		Roles:   req.Roles,
	})
	stopped := ""
	switch {
	case errors.Is(err, password.ErrBusy):
		stopped = codeSignInBusy.String()
		h.log.WarnContext(r.Context(), "an import stopped for lack of a hashing slot",
			"created", len(result.Created), "not_imported", len(result.NotImported))
	case err != nil:
		h.fail(w, r, err)
		return
	}

	created := make([]createResponse, 0, len(result.Created))
	for _, one := range result.Created {
		created = append(created, createResponse{
			User:            toUserResponse(one.User),
			OneTimePassword: one.OneTimePassword,
		})
	}
	skipped := make([]skippedAccount, 0, len(result.Skipped))
	for _, one := range result.Skipped {
		skipped = append(skipped, skippedAccount{Login: one.Login, Reason: one.Reason})
	}
	httpx.JSON(w, r, http.StatusOK, accountImportResponse{
		Created: created, Skipped: skipped, NotImported: emptyIfNil(result.NotImported), Stopped: stopped,
	})
}

type listResponse struct {
	Items []UserResponse `json:"items"`
	Total int            `json:"total"`
}

func (h *UsersHandler) list(w http.ResponseWriter, r *http.Request) {
	// An unknown status would filter by a value nothing has and return an empty
	// list; refuse it instead.
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(users.Statuses, status) {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
			fmt.Sprintf("status must be one of %v, got %q", users.Statuses, status))
		return
	}

	filter := users.Filter{
		Query:  r.URL.Query().Get("q"),
		Status: status,
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
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.UpdateProfile(r.Context(), identity.UserID, id, req.FullName, req.Email); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// blockRequest carries why the account is being blocked; an empty reason is
// refused (users.ErrReasonRequired).
type blockRequest struct {
	Reason string `json:"reason"`
}

func (h *UsersHandler) block(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	var req blockRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Block(r.Context(), identity.UserID, id, req.Reason); err != nil {
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

// deleteRequest carries why the account is being deleted; an empty reason is
// refused (users.ErrReasonRequired).
type deleteRequest struct {
	Reason string `json:"reason"`
}

func (h *UsersHandler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	var req deleteRequest
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Delete(r.Context(), identity.UserID, id, req.Reason); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *UsersHandler) restore(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.Restore(r.Context(), identity.UserID, id); err != nil {
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

// unlockSignIn clears the account's sign-in throttling, so an owner locked out
// by someone else's guesses can sign in now. The trail records who did it.
func (h *UsersHandler) unlockSignIn(w http.ResponseWriter, r *http.Request) {
	id, ok := h.accountID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.unlocker.UnlockSignIn(r.Context(), identity.UserID, id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
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
	if !decodeBody(w, r, &req) {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.ReplaceRoles(r.Context(), identity.UserID, id, req.Roles); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *UsersHandler) accountID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, userIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidUserID, "User identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// fail maps a service error to a response; anything unknown is a 500 with the
// detail only in the log.
func (h *UsersHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if usersErrors.answer(w, r, h.log, err) {
		return
	}
	switch {
	case errors.Is(err, password.ErrBusy):
		// Issuing a password waits longer for a hashing slot than sign-in does,
		// and still ran out (or the request ended). A single change writes
		// nothing; an import keeps the rows it already created.
		busy(w, r)
	default:
		h.log.ErrorContext(r.Context(), "account operation failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

func intParam(r *http.Request, name string) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return 0
	}
	return value
}
