package api

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/microsoft"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// The Microsoft endpoints' budget, each on its own, refusals included
// (CLAUDE.md rule 13). A class signs in from behind one address, so it is
// as generous as sign-up's.
const (
	MicrosoftSignInsPerAddress = 120
	microsoftAddressWindow     = 10 * time.Minute
)

// maxAuthorizationCodeLength bounds the code the callback exchanges;
// Microsoft's are around a kilobyte.
const maxAuthorizationCodeLength = 8 << 10

// MicrosoftSignIn is what the Microsoft handler is built from. Client nil
// means the connector is not configured.
type MicrosoftSignIn struct {
	Client     *microsoft.Client
	States     *microsoft.StateSealer
	Accounts   *federation.Service
	Auth       *auth.Service
	Limiter    *auth.Limiter
	Middleware *auth.Middleware
	Cookies    auth.CookieWriter
	Log        *slog.Logger
}

// MicrosoftHandler serves sign-in with Microsoft: the start, which sends the
// browser to Microsoft, and the callback it comes back to. Both are browser
// navigations, so every outcome is a redirect, a failure to the sign-in page
// with a code.
type MicrosoftHandler struct {
	cfg MicrosoftSignIn
}

func NewMicrosoftHandler(cfg MicrosoftSignIn) *MicrosoftHandler {
	return &MicrosoftHandler{cfg: cfg}
}

// Mount registers the routes, which need no session, only when the
// connector is configured; otherwise they do not exist.
func (h *MicrosoftHandler) Mount(r chi.Router) {
	if h.cfg.Client == nil {
		return
	}
	r.Group(func(r chi.Router) {
		r.Use(noStore)
		r.Get("/auth/microsoft/start", h.start)
		r.Get("/auth/microsoft/callback", h.callback)
	})
}

func (h *MicrosoftHandler) start(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r, "microsoft_start:ip:"+addressKey(r)) {
		return
	}
	st, err := microsoft.NewState(r.URL.Query().Get("return"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.cfg.Cookies.SetMicrosoftState(w, h.cfg.States.Seal(st), microsoft.StateLifetime)
	redirect(w, h.cfg.Client.AuthCodeURL(st))
}

// callback spends the address before anything is read, and so before the
// exchange, the one outbound call. The state cookie is cleared first, so
// every outcome spends it.
func (h *MicrosoftHandler) callback(w http.ResponseWriter, r *http.Request) {
	h.cfg.Cookies.ClearMicrosoftState(w)
	if !h.admit(w, r, "microsoft_callback:ip:"+addressKey(r)) {
		return
	}
	query := r.URL.Query()
	if refusal := query.Get("error"); refusal != "" {
		h.fail(w, r, fmt.Errorf("%w: Microsoft answered %.64q", microsoft.ErrSignInRefused, refusal))
		return
	}
	st, err := h.state(r, query.Get("state"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	code := query.Get("code")
	if code == "" || len(code) > maxAuthorizationCodeLength {
		h.fail(w, r, fmt.Errorf("%w: the callback carries no usable code", microsoft.ErrSignInRefused))
		return
	}

	claims, err := h.cfg.Client.Exchange(r.Context(), code, st)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	account, err := h.cfg.Accounts.Resolve(r.Context(), claims)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	previousToken, _ := h.cfg.Middleware.SessionToken(r)
	result, err := h.cfg.Auth.SignInAs(r.Context(), auth.SignInAsCommand{
		User: account, Method: auth.MethodMicrosoft,
		IP: httpx.ClientIP(r), UserAgent: r.UserAgent(), PreviousToken: previousToken,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.cfg.Cookies.Set(w, result.Token, h.cfg.Auth.Sessions().CookieLifetime())
	if result.DeviceToken != "" {
		h.cfg.Cookies.SetDevice(w, result.DeviceToken, h.cfg.Auth.DeviceCookieLifetime())
	}
	// Federation drops a one-time password it finds; one still held answers
	// every other screen with password_change_required.
	if result.MustChangePassword {
		redirect(w, passwordChangePage)
		return
	}
	redirect(w, st.Return)
}

// passwordChangePage is the interface's password change screen
// (frontend/app/(account)/password).
const passwordChangePage = "/password"

// state opens the one state cookie and checks that Microsoft sent back the
// state it holds, so a callback this browser did not start is refused.
func (h *MicrosoftHandler) state(r *http.Request, returned string) (microsoft.State, error) {
	cookies := r.CookiesNamed(h.cfg.Cookies.MicrosoftStateName())
	if len(cookies) != 1 {
		return microsoft.State{}, fmt.Errorf("%w: the browser holds %d state cookies", microsoft.ErrStateInvalid, len(cookies))
	}
	st, err := h.cfg.States.Open(cookies[0].Value)
	if err != nil {
		return microsoft.State{}, err
	}
	if subtle.ConstantTimeCompare([]byte(returned), []byte(st.State)) != 1 {
		return microsoft.State{}, fmt.Errorf("%w: the callback's state is not the cookie's", microsoft.ErrStateInvalid)
	}
	return st, nil
}

// admit spends the address budget. A refusal, or a limiter that cannot
// count, sends the browser to the sign-in page.
func (h *MicrosoftHandler) admit(w http.ResponseWriter, r *http.Request, subject string) bool {
	allowed, err := h.cfg.Limiter.Allow(r.Context(), subject, MicrosoftSignInsPerAddress, microsoftAddressWindow)
	if err != nil {
		h.cfg.Log.ErrorContext(r.Context(), "could not check the Microsoft sign-in rate limit", "error", err)
	}
	if !allowed {
		redirect(w, signInPage(codeExternalSignInFailed))
		return false
	}
	return true
}

// fail sends the browser to the sign-in page with err's code. Neither the
// code nor a token is ever logged: no error here carries one.
func (h *MicrosoftHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	code, outage, _ := externalSignInCode(err)
	if outage {
		h.cfg.Log.ErrorContext(r.Context(), "a Microsoft sign-in failed", "error", err)
	} else {
		h.cfg.Log.WarnContext(r.Context(), "a Microsoft sign-in was refused", "error", err)
	}
	redirect(w, signInPage(code))
}

func signInPage(code httpx.Code) string {
	return "/login?error=" + code.String()
}

// redirect answers 302 to target as it is; http.Redirect would clean a path
// the state already vetted.
func redirect(w http.ResponseWriter, target string) {
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}
