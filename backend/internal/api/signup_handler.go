package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/i18n"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/signup"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// Each sign-up endpoint's budget, its own subjects for each, refusals
// included (CLAUDE.md rule 13).
const (
	// SignupsPerAddress is per address in signupAddressWindow; a class signs
	// up from behind one address, so it is generous.
	SignupsPerAddress   = 120
	signupAddressWindow = 10 * time.Minute
	// SignupsPerEmail is per lowercased email in an hour, so neither an inbox
	// nor a waiting sign-up can be worn down from many addresses.
	SignupsPerEmail   = 5
	signupEmailWindow = time.Hour
)

// signupBodyBytes bounds a sign-up body as it arrives. The password is at
// most password.MaxLength bytes even escaped as \u00XX; the other fields
// refuse control characters, so a legitimate body fits with room to spare.
const signupBodyBytes = 8 << 10

// signupTokenLength is the length of every token Start answers; a cookie of
// any other length is no token.
var signupTokenLength = base64.RawURLEncoding.EncodedLen(signup.TokenBytes)

// SignupHandler serves self-service sign-up: the form, the confirmation of
// the mailed code, which signs the new account in, and a fresh code. The
// token binding a sign-up to its browser travels only in the sign-up cookie.
type SignupHandler struct {
	signups *signup.Service
	auth    *auth.Service
	limiter *auth.Limiter
	mail    *MailBudget
	mw      *auth.Middleware
	cookies auth.CookieWriter
	log     *slog.Logger
}

func NewSignupHandler(signups *signup.Service, authService *auth.Service, limiter *auth.Limiter, mail *MailBudget, mw *auth.Middleware, cookies auth.CookieWriter, log *slog.Logger) *SignupHandler {
	return &SignupHandler{signups: signups, auth: authService, limiter: limiter, mail: mail, mw: mw, cookies: cookies, log: log}
}

// Mount registers the routes under /public/, which needs no session, only
// when the installation offers sign-up; otherwise they do not exist.
func (h *SignupHandler) Mount(r chi.Router) {
	if !h.signups.Offered() {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(noStore)
		r.Post("/public/signup", h.start)
		r.Post("/public/signup/confirm", h.confirm)
		r.Post("/public/signup/resend", h.resend)
	})
}

type signupStartRequest struct {
	FullName string `json:"full_name"`
	Login    string `json:"login"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type signupConfirmRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

type signupResendRequest struct {
	Email string `json:"email"`
}

// signupSentResponse answers a start and a resend alike, for an address an
// account holds as for a new one.
type signupSentResponse struct {
	Status string `json:"status"`
	Email  string `json:"email"`
}

type signupConfirmedResponse struct {
	MustChangePassword bool `json:"must_change_password"`
}

// The address is spent before the body is read, and the email, an unbounded
// key, only once bounded and for a caller the address admitted (CLAUDE.md
// rule 5); then the installation's mail budget; all before the password hash.
func (h *SignupHandler) start(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r, "signup:ip:"+addressKey(r), SignupsPerAddress, signupAddressWindow) {
		return
	}
	var req signupStartRequest
	if !decodeBodyWithin(w, r, &req, signupBodyBytes) {
		return
	}
	email := signupEmail(req.Email)
	if !withinBounds(w, r,
		fieldBound{"full_name", strings.TrimSpace(req.FullName), users.MaxFullNameLength},
		fieldBound{"login", strings.TrimSpace(req.Login), users.MaxLoginLength},
		fieldBound{"email", email, users.MaxEmailLength},
		fieldBound{"password", req.Password, password.MaxLength},
	) {
		return
	}
	if !h.admit(w, r, "signup:email:"+email, SignupsPerEmail, signupEmailWindow) {
		return
	}
	if !admitMail(w, r, h.mail, h.log) {
		return
	}

	started, err := h.signups.Start(r.Context(), signup.StartCommand{
		FullName: req.FullName, Login: req.Login, Email: req.Email, Password: req.Password,
		Lang: readerMailLanguage(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.cookies.SetSignup(w, started.Token, signup.CodeLifetime)
	httpx.JSON(w, r, http.StatusAccepted, signupSentResponse{Status: "code_sent", Email: email})
}

// confirm makes the account and signs it in. SignInAs does not throttle, so
// both budgets are spent before the code is checked.
func (h *SignupHandler) confirm(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r, "signup_confirm:ip:"+addressKey(r), SignupsPerAddress, signupAddressWindow) {
		return
	}
	var req signupConfirmRequest
	if !decodeBodyWithin(w, r, &req, signupBodyBytes) {
		return
	}
	email := signupEmail(req.Email)
	if !withinBounds(w, r,
		fieldBound{"email", email, users.MaxEmailLength},
		fieldBound{"code", req.Code, signup.CodeDigits},
	) {
		return
	}
	if !h.admit(w, r, "signup_confirm:email:"+email, SignupsPerEmail, signupEmailWindow) {
		return
	}

	account, err := h.signups.Confirm(r.Context(), signup.ConfirmCommand{
		Email: req.Email, Code: req.Code, Token: h.signupToken(r),
	})
	if err != nil {
		h.failFrom(w, r, confirmAnswers, err)
		return
	}
	// The sign-up is spent whether or not the sign-in below succeeds.
	h.cookies.ClearSignup(w)

	previousToken, _ := h.mw.SessionToken(r)
	result, err := h.auth.SignInAs(r.Context(), auth.SignInAsCommand{
		User: account, Method: auth.MethodSignup,
		IP: httpx.ClientIP(r), UserAgent: r.UserAgent(), PreviousToken: previousToken,
	})
	if err != nil {
		h.failFrom(w, r, confirmAnswers, err)
		return
	}
	h.cookies.Set(w, result.Token, h.auth.Sessions().CookieLifetime())
	if result.DeviceToken != "" {
		h.cookies.SetDevice(w, result.DeviceToken, h.auth.DeviceCookieLifetime())
	}
	httpx.JSON(w, r, http.StatusOK, signupConfirmedResponse{MustChangePassword: result.MustChangePassword})
}

func (h *SignupHandler) resend(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r, "signup_resend:ip:"+addressKey(r), SignupsPerAddress, signupAddressWindow) {
		return
	}
	var req signupResendRequest
	if !decodeBodyWithin(w, r, &req, signupBodyBytes) {
		return
	}
	email := signupEmail(req.Email)
	if !withinBounds(w, r, fieldBound{"email", email, users.MaxEmailLength}) {
		return
	}
	if !h.admit(w, r, "signup_resend:email:"+email, SignupsPerEmail, signupEmailWindow) {
		return
	}
	if !admitMail(w, r, h.mail, h.log) {
		return
	}

	token := h.signupToken(r)
	if err := h.signups.Resend(r.Context(), signup.ResendCommand{
		Email: req.Email, Token: token, Lang: readerMailLanguage(r),
	}); err != nil {
		h.fail(w, r, err)
		return
	}
	// A new code opens a new window, so the browser keeps its token as long.
	if token != "" {
		h.cookies.SetSignup(w, token, signup.CodeLifetime)
	}
	httpx.JSON(w, r, http.StatusAccepted, signupSentResponse{Status: "code_sent", Email: email})
}

// signupToken is the sign-up cookie's token, or "" for none, which the
// service answers as a wrong code.
func (h *SignupHandler) signupToken(r *http.Request) string {
	cookie, err := r.Cookie(h.cookies.SignupName())
	if err != nil || len(cookie.Value) != signupTokenLength {
		return ""
	}
	return cookie.Value
}

// signupEmail is the address as the service keys a sign-up, and so as the
// limiter does.
func signupEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// fieldBound is a request field and the most bytes the service accepts in
// it.
type fieldBound struct {
	name  string
	value string
	max   int
}

// withinBounds answers 400 for the first field over its bound and reports
// whether the handler may go on.
func withinBounds(w http.ResponseWriter, r *http.Request, fields ...fieldBound) bool {
	for _, f := range fields {
		if len(f.value) > f.max {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
				fmt.Sprintf("%s must be at most %d bytes", f.name, f.max))
			return false
		}
	}
	return true
}

// readerMailLanguage is the mail language the reader asks for (?lang=, then
// Accept-Language), or "" when the texts are written in none of theirs,
// which the service reads as the installation's mail language.
func readerMailLanguage(r *http.Request) string {
	var preferred []string
	if explicit := r.URL.Query().Get("lang"); explicit != "" {
		preferred = append(preferred, explicit)
	}
	preferred = append(preferred, i18n.ParseAcceptLanguage(r.Header.Get("Accept-Language"))...)
	available := make([]string, 0, len(mailing.Languages())+1)
	for _, lang := range mailing.Languages() {
		available = append(available, string(lang))
	}
	// Match answers its fallback when no preference is available; the empty
	// entry is that fallback, so nothing preferred means "".
	return i18n.Match(preferred, append(available, ""), "")
}

func (h *SignupHandler) admit(w http.ResponseWriter, r *http.Request, subject string, limit int, window time.Duration) bool {
	allowed, err := h.limiter.Allow(r.Context(), subject, limit, window)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the sign-up rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		httpx.Error(w, r, http.StatusTooManyRequests, codeTooManyAttempts,
			"Too many sign-up attempts from this address or for this email; wait before trying again")
		return false
	}
	return true
}

// signupAnswers is what Start and Resend answer from.
var signupAnswers = joined(signupErrors, usersErrors)

// confirmAnswers is what Confirm answers from. An account that took the
// address after Start leaves Confirm a spent sign-up, answered as an expired
// one: email_taken would tell whoever holds the code that the address has an
// account.
var confirmAnswers = signupAnswers.with(errorRow{
	err: users.ErrEmailTaken, status: http.StatusGone, code: codeSignupCodeExpired,
	message: signupExpiredMessage,
})

func (h *SignupHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.failFrom(w, r, signupAnswers, err)
}

func (h *SignupHandler) failFrom(w http.ResponseWriter, r *http.Request, answers errorTable, err error) {
	if answers.answer(w, r, h.log, err) {
		return
	}
	switch {
	// SignInAs refuses an account blocked between its making and its
	// sign-in.
	case errors.Is(err, auth.ErrAccountBlocked):
		httpx.Error(w, r, http.StatusForbidden, codeAccountBlocked, "This account is blocked")
	// The chosen password found no hashing slot in the sign-in wait.
	case errors.Is(err, password.ErrBusy):
		busy(w, r)
	default:
		h.log.ErrorContext(r.Context(), "the sign-up request failed", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}

// noStore keeps every sign-up answer out of caches, refusals included,
// whatever router serves the routes.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
