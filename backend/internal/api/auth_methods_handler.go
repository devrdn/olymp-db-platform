package api

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"

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

// authMethodsReadTimeout bounds the one read of the switches the page waits
// for.
const authMethodsReadTimeout = 2 * time.Second

// AuthMethodsHandler tells the interface which ways in this installation
// offers and which email domain is its own, so no installation's values are
// written into the interface.
type AuthMethodsHandler struct {
	cfg AuthMethods
	now func() time.Time

	timeout time.Duration
	reads   singleflight.Group

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
// time.Now and ReadTimeout zero is authMethodsReadTimeout.
type AuthMethods struct {
	Signups, Recoveries offerer
	Providers           []string
	Switches            SignInSwitches
	TrustedEmailDomain  string
	Log                 *slog.Logger
	Now                 func() time.Time
	ReadTimeout         time.Duration
}

func NewAuthMethodsHandler(cfg AuthMethods) *AuthMethodsHandler {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	timeout := cfg.ReadTimeout
	if timeout <= 0 {
		timeout = authMethodsReadTimeout
	}
	return &AuthMethodsHandler{cfg: cfg, now: now, timeout: timeout}
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

// switchedOff answers from the cache while it is fresh. A burst of misses
// shares one read, detached from the first caller's context so that a client
// going away is not logged as an outage, and bounded by its own deadline so a
// slow database cannot hold the page. A read that fails is logged and
// answered as "no providers" (false), and is not stored: the next request
// after it asks again.
func (h *AuthMethodsHandler) switchedOff(r *http.Request) (map[string]bool, bool) {
	if off, ok := h.cached(); ok {
		return off, true
	}
	v, err, _ := h.reads.Do("switches", func() (any, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.timeout)
		defer cancel()
		off, err := h.cfg.Switches.DisabledSignInProviders(ctx)
		if err != nil {
			h.cfg.Log.ErrorContext(ctx, "could not read the sign-in switches", "error", err)
			return nil, err
		}
		h.mu.Lock()
		h.off, h.readAt, h.hasRead = off, h.now(), true
		h.mu.Unlock()
		return off, nil
	})
	if err != nil {
		return nil, false
	}
	return v.(map[string]bool), true
}

func (h *AuthMethodsHandler) cached() (map[string]bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.off, h.hasRead && h.now().Sub(h.readAt) < authMethodsSwitchTTL
}
