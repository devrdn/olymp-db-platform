package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/connector"
	"github.com/devrdn/db-contest/backend/internal/federation"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// Each provider's endpoints have this budget, each route on its own,
// refusals included (CLAUDE.md rule 13). A class signs in from behind one
// address, so it is as generous as sign-up's.
const (
	ExternalSignInsPerAddress = 120
	externalAddressWindow     = 10 * time.Minute
)

// maxAuthorizationCodeLength bounds the code the callback exchanges;
// providers' are at most a few kilobytes.
const maxAuthorizationCodeLength = 8 << 10

// SignInConnector is one provider's side of the flow: where to send the
// browser, and what the code it comes back with proves.
type SignInConnector interface {
	AuthCodeURL(st connector.State) string
	Exchange(ctx context.Context, code string, st connector.State) (federation.Claims, error)
}

// SignInSwitches says which providers an administrator switched off
// (settings.Service).
type SignInSwitches interface {
	DisabledSignInProviders(ctx context.Context) (map[string]bool, error)
}

// errSignInSwitchedOff refuses a start or callback of a provider an
// administrator switched off; it answers external_signin_unavailable.
var errSignInSwitchedOff = errors.New("an administrator switched this sign-in off")

// SignInProvider is one way to sign in through someone else's directory.
// Connector nil means it is not configured and has no routes; Variables name
// the environment variables that configure it. States must be sealed for ID,
// so one provider's state never opens at another's callback.
type SignInProvider struct {
	ID        string
	Connector SignInConnector
	States    *connector.StateSealer
	Variables []string
}

// ExternalSignIn is what the external sign-in handler is built from.
type ExternalSignIn struct {
	Providers  []SignInProvider
	Accounts   *federation.Service
	Auth       *auth.Service
	Limiter    *auth.Limiter
	Middleware *auth.Middleware
	Cookies    auth.CookieWriter
	Switches   SignInSwitches
	Log        *slog.Logger
}

// CallbackPath is where a provider sends the browser back, below PUBLIC_URL;
// the application registration names the same address.
func CallbackPath(provider string) string {
	return "/api/v1/auth/" + provider + "/callback"
}

// ExternalSignInHandler serves sign-in through each configured provider: the
// start, which sends the browser to the provider, and the callback it comes
// back to. Both are browser navigations, so every outcome is a redirect, a
// failure to the sign-in page with a code.
type ExternalSignInHandler struct {
	cfg ExternalSignIn
}

// NewExternalSignInHandler panics when a provider cannot be served: an id
// no identity can carry, or a connector with nothing to seal its state, or no
// switches to read. Each is a mistake of the composition root, found at
// start-up.
func NewExternalSignInHandler(cfg ExternalSignIn) *ExternalSignInHandler {
	for _, p := range cfg.Providers {
		if !slices.Contains(users.Providers, p.ID) {
			panic(fmt.Sprintf("api: sign-in provider %q is not one of users.Providers", p.ID))
		}
		if p.Connector != nil && (p.States == nil || cfg.Switches == nil) {
			panic(fmt.Sprintf("api: sign-in provider %q has a connector but no state seal or switches", p.ID))
		}
	}
	return &ExternalSignInHandler{cfg: cfg}
}

// Mount registers the routes of every provider with a connector, which need
// no session; the others do not exist.
func (h *ExternalSignInHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(noStore)
		for _, p := range h.cfg.Providers {
			if p.Connector == nil {
				continue
			}
			flow := signInFlow{cfg: h.cfg, provider: p}
			r.Get("/auth/"+p.ID+"/start", flow.start)
			r.Get("/auth/"+p.ID+"/callback", flow.callback)
		}
	})
}

// signInFlow is the handler bound to one provider.
type signInFlow struct {
	cfg      ExternalSignIn
	provider SignInProvider
}

func (h signInFlow) start(w http.ResponseWriter, r *http.Request) {
	if !h.admit(w, r, h.provider.ID+"_start:ip:"+addressKey(r)) {
		return
	}
	if !h.switchedOn(w, r) {
		return
	}
	st, err := connector.NewState(r.URL.Query().Get("return"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.cfg.Cookies.SetState(w, h.provider.ID, h.provider.States.Seal(st), connector.StateLifetime)
	redirect(w, h.provider.Connector.AuthCodeURL(st))
}

// callback spends the address before anything is read, and so before the
// exchange, the one outbound call. The state cookie is cleared first, so
// every outcome spends it.
func (h signInFlow) callback(w http.ResponseWriter, r *http.Request) {
	h.cfg.Cookies.ClearState(w, h.provider.ID)
	if !h.admit(w, r, h.provider.ID+"_callback:ip:"+addressKey(r)) {
		return
	}
	if !h.switchedOn(w, r) {
		return
	}
	query := r.URL.Query()
	if refusal := query.Get("error"); refusal != "" {
		h.fail(w, r, fmt.Errorf("%w: %s answered %.64q", connector.ErrSignInRefused, h.provider.ID, refusal))
		return
	}
	st, err := h.state(r, query.Get("state"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	code := query.Get("code")
	if code == "" || len(code) > maxAuthorizationCodeLength {
		h.fail(w, r, fmt.Errorf("%w: the callback carries no usable code", connector.ErrSignInRefused))
		return
	}

	claims, err := h.provider.Connector.Exchange(r.Context(), code, st)
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
		User: account, Method: h.provider.ID,
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

// state opens the one state cookie and checks that the provider sent back the
// state it holds, so a callback this browser did not start is refused.
func (h signInFlow) state(r *http.Request, returned string) (connector.State, error) {
	cookies := r.CookiesNamed(h.cfg.Cookies.StateName(h.provider.ID))
	if len(cookies) != 1 {
		return connector.State{}, fmt.Errorf("%w: the browser holds %d state cookies", connector.ErrStateInvalid, len(cookies))
	}
	st, err := h.provider.States.Open(cookies[0].Value)
	if err != nil {
		return connector.State{}, err
	}
	if subtle.ConstantTimeCompare([]byte(returned), []byte(st.State)) != 1 {
		return connector.State{}, fmt.Errorf("%w: the callback's state is not the cookie's", connector.ErrStateInvalid)
	}
	return st, nil
}

// admit spends the address budget. A refusal, or a limiter that cannot
// count, sends the browser to the sign-in page.
func (h signInFlow) admit(w http.ResponseWriter, r *http.Request, subject string) bool {
	allowed, err := h.cfg.Limiter.Allow(r.Context(), subject, ExternalSignInsPerAddress, externalAddressWindow)
	if err != nil {
		h.cfg.Log.ErrorContext(r.Context(), "could not check the sign-in rate limit", "provider", h.provider.ID, "error", err)
	}
	if !allowed {
		redirect(w, signInPage(codeExternalSignInFailed))
		return false
	}
	return true
}

// switchedOn reads the switches uncached, so a switch-off holds at the next
// start or callback. A switch that cannot be read refuses the sign-in.
func (h signInFlow) switchedOn(w http.ResponseWriter, r *http.Request) bool {
	off, err := h.cfg.Switches.DisabledSignInProviders(r.Context())
	if err != nil {
		h.fail(w, r, fmt.Errorf("read the sign-in switches: %w", err))
		return false
	}
	if off[h.provider.ID] {
		h.fail(w, r, errSignInSwitchedOff)
		return false
	}
	return true
}

// fail sends the browser to the sign-in page with err's code. Neither the
// code nor a token is ever logged: no error here carries one.
func (h signInFlow) fail(w http.ResponseWriter, r *http.Request, err error) {
	code, outage, _ := externalSignInCode(err)
	// Only when the request's own context ended, so a cancellation of ours
	// is still an outage.
	clientLeft := r.Context().Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	if outage && !clientLeft {
		h.cfg.Log.ErrorContext(r.Context(), "a sign-in failed", "provider", h.provider.ID, "error", err)
	} else {
		h.cfg.Log.WarnContext(r.Context(), "a sign-in was refused", "provider", h.provider.ID, "error", err)
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
