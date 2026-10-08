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

// userIDParam names the account in the URL.
const userIDParam = "userID"

// RoleCatalog lists the roles an account may hold.
//
// Declared here, by the consumer, and one method wide: this screen offers a
// list to pick from and has no business with anything else about roles.
type RoleCatalog interface {
	Roles(ctx context.Context) ([]users.Role, error)
}

// SignInUnlocker clears an account's sign-in throttling — the one method of
// auth.Service this handler needs, declared here by the consumer.
type SignInUnlocker interface {
	UnlockSignIn(ctx context.Context, actorID, userID uuid.UUID) error
}

// UsersHandler serves the account management endpoints. Every route requires the
// installation-wide users.manage permission: these operations are not scoped
// to a contest, and running one must never grant power over accounts.
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
	// A sibling of /users rather than /users/roles: it describes the
	// installation, not an account. Behind the same permission, because which
	// roles exist is a description of how this university is organised and it
	// goes with the screen that uses it.
	r.Route("/roles", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))
		r.Get("/", h.listRoles)
	})

	r.Route("/users", func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequirePermission(rbac.PermissionUsersManage))

		r.Get("/", h.list)
		r.Post("/", h.create)
		r.Post("/import", h.importRoster)

		// A sibling of the single-account routes rather than a query on them:
		// the selection is the subject. Registered before the /{userID} group
		// so chi resolves the literal "bulk" segment here rather than reading
		// it as an account identifier.
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

// listRoles publishes the roles an account may hold.
//
// It exists so the interface never hard-codes "student, organizer, admin".
// Roles are rows precisely so a new one is data; a list repeated in the client
// takes that back, and the day somebody adds a role one of the two copies is
// wrong without saying so.
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
	// StatusReason, StatusChangedAt and StatusChangedBy explain the current
	// status: why, when, and by whom. All three are empty for an account
	// nobody has ever blocked or deleted — a fresh account has nothing to
	// account for, and the account card reads their absence as exactly that
	// rather than as an empty history to render.
	StatusReason    string `json:"status_reason,omitempty"`
	StatusChangedAt string `json:"status_changed_at,omitempty"`
	// StatusChangedBy is the actor's id.
	StatusChangedBy string `json:"status_changed_by,omitempty"`
	// StatusChangedByLogin is that actor's login, resolved by the repository's
	// own query (a LEFT JOIN against users, in internal/postgres/users.go) so
	// the account card can name them without a second request for one login.
	// Empty exactly when StatusChangedBy is.
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

// toIdentityResponse is what a signed-in account learns about itself.
//
// toUserResponse is shared with the account-management screens, which is
// what let status_changed_at and status_changed_by ride along into the
// sign-in response the account owner receives: an administrator's UUID and
// the moment they acted, neither of which is that account owner's business.
// The reason text needs no such stripping — an active account's reason is
// always empty — but the metadata is cleared explicitly here so a future
// field on UserResponse defaults to hidden on this path instead of leaking
// by omission.
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
	// OneTimePassword is shown once, here. It is not stored in clear and
	// cannot be fetched later; a lost one is reset.
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

// importRequest is a whole group at once.
//
// Accounts are created by an administrator rather than by self-registration
// (§7), and a group arrives as a list from the department. One request per
// student is thirty round trips and thirty chances to lose one.
type importRequest struct {
	Rows []importRow `json:"rows"`
	// Roles every created account receives, typically the single student role.
	Roles []string `json:"roles"`
}

type importRow struct {
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email"`
}

// importResponse reports the outcome row by row.
//
// Partial success, honestly: one duplicate must not reject the other
// twenty-nine, and whoever pasted the list has to see which line to fix. The
// one-time passwords appear here and nowhere else — they are not stored in
// clear and cannot be fetched later.
//
// An import the hasher's load stops partway is still answered 200 with what it
// did: the accounts created before it stopped exist, and their passwords are
// shown here or never. NotImported names every row it never reached, and
// Stopped carries the code saying why, so the rest can be imported again.
// Nothing else that stops an import is answered this way: an outage is an
// error, not a result.
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
	// An unreadable status filters by a value nothing has, so the register
	// comes back empty — "no account matches", a true answer to a question
	// nobody asked. Refused instead, the same way the contest listing refuses
	// an unreadable `enrolled`.
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

// blockRequest carries why the account is being blocked. The service refuses
// an empty reason with users.ErrReasonRequired, which fail maps to a 400.
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

// deleteRequest carries why the account is being deleted. The service refuses
// an empty reason with users.ErrReasonRequired, which fail maps to a 400.
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

// unlockSignIn clears the account's sign-in throttling, so an owner shut out
// by somebody else's wrong guesses — a rival behind the same lab address, a
// guess spread across many — can try again now rather than when the window
// runs out. Answered 204: there is nothing to return but that it happened,
// and the trail records who did it.
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

// accountID reads and validates the account in the URL.
func (h *UsersHandler) accountID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, userIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidUserID, "User identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// fail maps a service error onto a response. Anything unrecognised becomes a
// 500 with the detail kept in the log, never in the body.
func (h *UsersHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if usersErrors.answer(w, r, h.log, err) {
		return
	}
	switch {
	case errors.Is(err, password.ErrBusy):
		// Issuing a password waits far longer for a hashing slot than a
		// sign-in does, and still ran out — or the administrator's request
		// ended first. The operation stops there: a single change writes
		// nothing, and an import keeps the rows it had already created, as it
		// does for any other failure partway through a roster.
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
