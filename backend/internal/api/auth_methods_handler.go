package api

import (
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// offerer is a way in that may be switched off (signup.Service,
// recovery.Service).
type offerer interface {
	Offered() bool
}

// authMethodsSwitchTTL is how long an answer about the administrator's
// switches is reused: the page is read by everyone who opens the sign-in
// screen, and a switch-off showing five seconds late costs nothing, since
// start and callback read it afresh.
const authMethodsSwitchTTL = 5 * time.Second

// AuthMethodsHandler tells the interface which ways in this installation
// offers and which email domain is its own, so no installation's values are
// written into the interface.
type AuthMethodsHandler struct {
	cfg AuthMethods
	now func() time.Time

	mu      sync.Mutex
	off     map[string]bool
	readAt  time.Time
	hasRead bool
}

type authMethodsResponse struct {
	PasswordSignup bool `json:"password_signup"`
	PasswordReset  bool `json:"password_reset"`
	// Microsoft is kept for interfaces that predate Providers.
	Microsoft          bool           `json:"microsoft"`
	Providers          []providerInfo `json:"providers"`
	TrustedEmailDomain string         `json:"trusted_email_domain"`
}

type providerInfo struct {
	ID string `json:"id"`
}

// AuthMethods is what the methods handler is built from. Providers are the
// configured provider ids, in the order they are offered; Now nil is
// time.Now.
type AuthMethods struct {
	Signups, Recoveries offerer
	Providers           []string
	Switches            SignInSwitches
	TrustedEmailDomain  string
	Log                 *slog.Logger
	Now                 func() time.Time
}

func NewAuthMethodsHandler(cfg AuthMethods) *AuthMethodsHandler {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &AuthMethodsHandler{cfg: cfg, now: now}
}

// Mount registers GET /auth/methods, which needs no session. The browser
// does not cache it; the switches behind it are cached by the process for
// authMethodsSwitchTTL.
func (h *AuthMethodsHandler) Mount(r chi.Router) {
	r.With(noStore).Get("/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, r, http.StatusOK, h.body(r))
	})
}

func (h *AuthMethodsHandler) body(r *http.Request) authMethodsResponse {
	off, ok := h.switchedOff(r)
	listed := make([]providerInfo, 0, len(h.cfg.Providers))
	if ok {
		for _, id := range h.cfg.Providers {
			if !off[id] {
				listed = append(listed, providerInfo{ID: id})
			}
		}
	}
	return authMethodsResponse{
		PasswordSignup: h.cfg.Signups.Offered(),
		PasswordReset:  h.cfg.Recoveries.Offered(),
		Microsoft:      slices.ContainsFunc(listed, func(p providerInfo) bool { return p.ID == users.ProviderMicrosoft }),
		Providers:      listed, TrustedEmailDomain: h.cfg.TrustedEmailDomain,
	}
}

// switchedOff answers from the cache while it is fresh. A read that fails is
// logged and answered as "no providers" (false), and not cached, so the next
// request asks again; the lock is held across the read so a burst after
// expiry makes one.
func (h *AuthMethodsHandler) switchedOff(r *http.Request) (map[string]bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hasRead && h.now().Sub(h.readAt) < authMethodsSwitchTTL {
		return h.off, true
	}
	off, err := h.cfg.Switches.DisabledSignInProviders(r.Context())
	if err != nil {
		h.cfg.Log.ErrorContext(r.Context(), "could not read the sign-in switches", "error", err)
		return nil, false
	}
	h.off, h.readAt, h.hasRead = off, h.now(), true
	return off, true
}
