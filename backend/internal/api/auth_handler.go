package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// PasswordChanger is the slice of account management the auth endpoints need.
// Declaring it here rather than depending on the concrete service keeps the
// coupling to what is actually used.
type PasswordChanger interface {
	ChangePassword(ctx context.Context, cmd users.ChangePasswordCommand) error
}

// AccountReader is the one method the profile endpoint needs: the descriptive
// fields an identity does not carry.
//
// The identity is assembled for authorisation, and holds what a guard decides
// on — an id, a login, a permission set. A person's name and roles are not
// that, and widening the identity to carry them would put display data on the
// value every middleware in the chain passes around.
type AccountReader interface {
	ByID(ctx context.Context, id uuid.UUID) (users.User, error)
}

// AuthHandler serves the authentication endpoints.
type AuthHandler struct {
	service   *auth.Service
	passwords PasswordChanger
	accounts  AccountReader
	mw        *auth.Middleware
	cookies   auth.CookieWriter
	log       *slog.Logger
}

// NewAuthHandler assembles the authentication endpoints.
func NewAuthHandler(service *auth.Service, passwords PasswordChanger, accounts AccountReader, mw *auth.Middleware, cookies auth.CookieWriter, log *slog.Logger) *AuthHandler {
	return &AuthHandler{
		service:   service,
		passwords: passwords,
		accounts:  accounts,
		mw:        mw,
		cookies:   cookies,
		log:       log,
	}
}

// Mount registers the routes under /auth.
func (h *AuthHandler) Mount(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/login", h.login)

		// Everything below needs a session.
		r.Group(func(r chi.Router) {
			r.Use(h.mw.Authenticate)
			r.Get("/me", h.me)
			r.Post("/logout", h.logout)
			r.Post("/password", h.changePassword)
		})
	})
}

type loginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type loginResponse struct {
	User               UserResponse `json:"user"`
	MustChangePassword bool         `json:"must_change_password"`
}

// login authenticates and starts a session.
//
// The session token goes into the cookie only. Returning it in the body would
// hand it to any script on the page and undo the point of HttpOnly.
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	result, err := h.service.Login(r.Context(), auth.LoginCommand{
		Login:     req.Login,
		Password:  req.Password,
		IP:        httpx.ClientIP(r),
		UserAgent: r.UserAgent(),
	})
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrInvalidCredentials):
		httpx.Error(w, r, http.StatusUnauthorized, codeInvalidCredentials, "Incorrect login or password")
		return
	case errors.Is(err, auth.ErrAccountBlocked):
		httpx.Error(w, r, http.StatusForbidden, codeAccountBlocked,
			"This account is blocked. Contact the organizers.")
		return
	case errors.Is(err, auth.ErrTooManyAttempts):
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyAttempts,
			"Too many attempts. Try again in a few minutes.")
		return
	case errors.Is(err, password.ErrBusy):
		busy(w, r)
		return
	default:
		h.log.ErrorContext(r.Context(), "login failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	h.cookies.Set(w, result.Token, h.service.Sessions().CookieLifetime())
	httpx.JSON(w, r, http.StatusOK, loginResponse{
		User:               toIdentityResponse(result.User),
		MustChangePassword: result.MustChangePassword,
	})
}

type meResponse struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	// FullName and Roles are what a profile screen shows. Permissions answer
	// "may I offer this button"; a name and a role answer "who am I looking
	// at", and initials taken from a login would read "II" for Ivan Ivanov.
	FullName    string   `json:"full_name"`
	Email       string   `json:"email,omitempty"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// me describes the signed-in account: what it may do, so the interface can hide
// what it must not offer, and who it is, so a profile can say so.
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, auth.CodeUnauthenticated, "Sign in to continue")
		return
	}

	permissions := make([]string, 0, len(identity.Permissions))
	for permission := range identity.Permissions {
		permissions = append(permissions, permission)
	}
	// Sorted so the response is stable between requests and easy to diff.
	slices.Sort(permissions)

	body := meResponse{
		ID:          identity.UserID.String(),
		Login:       identity.Login,
		Permissions: permissions,
	}

	// One indexed lookup, on the endpoint whose whole job is describing the
	// account. A failure here does not fail the request: routing depends on
	// the permissions above, and losing a display name must not lock somebody
	// out of an interface they are entitled to.
	if account, err := h.accounts.ByID(r.Context(), identity.UserID); err != nil {
		h.log.WarnContext(r.Context(), "could not read the account behind the session",
			"user_id", identity.UserID, "error", err)
	} else {
		body.FullName = account.FullName
		body.Email = account.Email
		body.Roles = account.Roles
	}

	httpx.JSON(w, r, http.StatusOK, body)
}

// logout ends the session and clears the cookie.
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
			h.log.ErrorContext(r.Context(), "logout failed", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
			return
		}
	}

	h.cookies.Clear(w)
	httpx.NoContent(w, r)
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// changePassword lets a signed-in user replace their own password.
func (h *AuthHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, auth.CodeUnauthenticated, "Sign in to continue")
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	// Throttled like a sign-in, because it verifies a password like one. The
	// session proves possession of a browser, not knowledge of the password,
	// and this is the endpoint where that difference is tested.
	switch err := h.service.AllowPasswordChange(r.Context(), identity.UserID); {
	case err == nil:
	case errors.Is(err, auth.ErrTooManyAttempts):
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyAttempts,
			"Too many attempts. Try again in a few minutes.")
		return
	default:
		h.log.ErrorContext(r.Context(), "password change throttle failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	err := h.passwords.ChangePassword(r.Context(), users.ChangePasswordCommand{
		UserID:      identity.UserID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	switch {
	case err == nil:
		h.service.ClearPasswordChangeThrottle(r.Context(), identity.UserID)
	case errors.Is(err, users.ErrWrongPassword):
		httpx.Error(w, r, http.StatusBadRequest, codeWrongPassword, "Current password is incorrect")
		return
	case errors.Is(err, users.ErrWeakPassword):
		httpx.Error(w, r, http.StatusBadRequest, codeWeakPassword, err.Error())
		return
	case errors.Is(err, users.ErrSamePassword):
		httpx.Error(w, r, http.StatusBadRequest, codeSamePassword, "Choose a password different from the current one")
		return
	case errors.Is(err, password.ErrBusy):
		busy(w, r)
		return
	default:
		h.log.ErrorContext(r.Context(), "password change failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	// The session that made the change was retired along with the others, so
	// the cookie is cleared and the user signs in with the new password.
	h.cookies.Clear(w)
	httpx.NoContent(w, r)
}

// busy answers a request whose password work found every hashing slot taken.
// 503 rather than 429: the caller did nothing wrong and is not being limited,
// the process is. Retry-After tells a client roughly when a slot is likely
// to be free.
func busy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	httpx.Error(w, r, http.StatusServiceUnavailable, codeSignInBusy,
		"The service is busy checking passwords. Try again in a moment.")
}
