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
type PasswordChanger interface {
	ChangePassword(ctx context.Context, cmd users.ChangePasswordCommand) error
}

// AccountReader reads the descriptive fields the identity does not carry. The
// identity holds only what authorisation decides on; display data stays off the
// value every middleware passes around.
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

// login authenticates and starts a session. The token goes only into the
// HttpOnly cookie; returning it in the body would expose it to page scripts.
func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeBody(w, r, &req) {
		return
	}

	// A browser the owner has signed in from is throttled on its own, not with
	// its shared address. A missing or unreadable cookie is no cookie.
	var deviceToken string
	if cookie, err := r.Cookie(auth.DeviceCookieName); err == nil {
		deviceToken = cookie.Value
	}

	// A successful sign-in replaces an existing session instead of leaving it
	// alive.
	var previousToken string
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		previousToken = cookie.Value
	}

	result, err := h.service.Login(r.Context(), auth.LoginCommand{
		Login:         req.Login,
		Password:      req.Password,
		IP:            httpx.ClientIP(r),
		UserAgent:     r.UserAgent(),
		DeviceToken:   deviceToken,
		PreviousToken: previousToken,
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
	if result.DeviceToken != "" {
		h.cookies.SetDevice(w, result.DeviceToken, h.service.DeviceCookieLifetime())
	}
	httpx.JSON(w, r, http.StatusOK, loginResponse{
		User:               toIdentityResponse(result.User),
		MustChangePassword: result.MustChangePassword,
	})
}

type meResponse struct {
	ID    string `json:"id"`
	Login string `json:"login"`
	// FullName and Roles are for the profile screen: permissions say what to
	// offer, a name says who this is.
	FullName    string   `json:"full_name"`
	Email       string   `json:"email,omitempty"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// me describes the signed-in account: its permissions, so the interface hides
// what it must not offer, and who it is.
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
	// Sorted, so the response is stable.
	slices.Sort(permissions)

	body := meResponse{
		ID:          identity.UserID.String(),
		Login:       identity.Login,
		Permissions: permissions,
	}

	// A failed lookup does not fail the request: routing depends on the
	// permissions, and losing a display name must not lock anyone out.
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

func (h *AuthHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpx.Error(w, r, http.StatusUnauthorized, auth.CodeUnauthenticated, "Sign in to continue")
		return
	}

	var req changePasswordRequest
	if !decodeBody(w, r, &req) {
		return
	}

	// Throttled like a sign-in because it verifies a password (CLAUDE.md rule
	// 4): a session proves possession of a browser, not knowledge of the
	// password.
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
	if usersErrors.answer(w, r, h.log, err) {
		return
	}
	switch {
	case err == nil:
		h.service.ClearPasswordChangeThrottle(r.Context(), identity.UserID)
	case errors.Is(err, password.ErrBusy):
		busy(w, r)
		return
	default:
		h.log.ErrorContext(r.Context(), "password change failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return
	}

	// The change retired every session, this one included, so the cookie is
	// cleared.
	h.cookies.Clear(w)
	httpx.NoContent(w, r)
}

// busy answers when every hashing slot is taken. 503, not 429: the process is
// at capacity, the caller is not being limited.
func busy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	httpx.Error(w, r, http.StatusServiceUnavailable, codeSignInBusy,
		"The service is busy checking passwords. Try again in a moment.")
}
