package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
)

// PasswordChanger is the slice of account management the auth endpoints need.
// Declaring it here rather than depending on the concrete service keeps the
// coupling to what is actually used.
type PasswordChanger interface {
	ChangePassword(ctx context.Context, cmd users.ChangePasswordCommand) error
}

// AuthHandler serves the authentication endpoints.
type AuthHandler struct {
	service   *auth.Service
	passwords PasswordChanger
	mw        *auth.Middleware
	cookies   auth.CookieWriter
	log       *slog.Logger
}

// NewAuthHandler assembles the authentication endpoints.
func NewAuthHandler(service *auth.Service, passwords PasswordChanger, mw *auth.Middleware, cookies auth.CookieWriter, log *slog.Logger) *AuthHandler {
	return &AuthHandler{service: service, passwords: passwords, mw: mw, cookies: cookies, log: log}
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
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
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
		httpx.Error(w, r, http.StatusUnauthorized, "invalid_credentials", "Incorrect login or password")
		return
	case errors.Is(err, auth.ErrAccountBlocked):
		httpx.Error(w, r, http.StatusForbidden, "account_blocked",
			"This account is blocked. Contact the organizers.")
		return
	case errors.Is(err, auth.ErrTooManyAttempts):
		httpx.Error(w, r, http.StatusTooManyRequests, "too_many_attempts",
			"Too many attempts. Try again in a few minutes.")
		return
	default:
		h.log.ErrorContext(r.Context(), "login failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	h.cookies.Set(w, result.Token, h.service.Sessions().TTL())
	httpx.JSON(w, r, http.StatusOK, loginResponse{
		User:               toUserResponse(result.User),
		MustChangePassword: result.MustChangePassword,
	})
}

type meResponse struct {
	ID          string   `json:"id"`
	Login       string   `json:"login"`
	Permissions []string `json:"permissions"`
}

// me describes the signed-in account and what it may do, so the interface can
// hide what it must not offer.
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue")
		return
	}

	permissions := make([]string, 0, len(identity.Permissions))
	for permission := range identity.Permissions {
		permissions = append(permissions, permission)
	}
	// Sorted so the response is stable between requests and easy to diff.
	slices.Sort(permissions)

	httpx.JSON(w, r, http.StatusOK, meResponse{
		ID:          identity.UserID.String(),
		Login:       identity.Login,
		Permissions: permissions,
	})
}

// logout ends the session and clears the cookie.
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil {
			h.log.ErrorContext(r.Context(), "logout failed", "error", err)
			httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
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
		httpx.Error(w, r, http.StatusUnauthorized, "unauthenticated", "Sign in to continue")
		return
	}

	var req changePasswordRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	err := h.passwords.ChangePassword(r.Context(), users.ChangePasswordCommand{
		UserID:      identity.UserID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
	})
	switch {
	case err == nil:
	case errors.Is(err, users.ErrWrongPassword):
		httpx.Error(w, r, http.StatusBadRequest, "wrong_password", "Current password is incorrect")
		return
	case errors.Is(err, users.ErrWeakPassword):
		httpx.Error(w, r, http.StatusBadRequest, "weak_password", err.Error())
		return
	case errors.Is(err, users.ErrSamePassword):
		httpx.Error(w, r, http.StatusBadRequest, "same_password", "Choose a password different from the current one")
		return
	default:
		h.log.ErrorContext(r.Context(), "password change failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}

	// The session that made the change was retired along with the others, so
	// the cookie is cleared and the user signs in with the new password.
	h.cookies.Clear(w)
	httpx.NoContent(w, r)
}
