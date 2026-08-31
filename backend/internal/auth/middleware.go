package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/logging"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// SessionCookieName carries the session token. The cookie is httpOnly, so
// script on the page cannot read it, which is the main reason the token lives
// here rather than in local storage.
const SessionCookieName = "dbcontest_session"

// contestIDParam is the URL parameter the contest-scoped middleware reads.
const contestIDParam = "contestID"

// passwordChangeExempt lists the endpoints an account still on its one-time
// password may reach: the way out (changing the password), the way back
// (logout) and the self-description the client needs to route to the form.
//
// Exact paths, and the mount prefix is stripped before comparing rather than
// matched by suffix. A suffix match reads the same for these three and is not
// the same rule: it exempts anything whose path happens to end in one of them,
// so a later /contests/{id}/auth/me would reopen the whole API to an account
// carrying somebody else's handover password — and nothing about adding that
// route would look like a security decision.
var passwordChangeExemptPaths = map[string]struct{}{
	"/auth/password": {},
	"/auth/logout":   {},
	"/auth/me":       {},
}

// apiMount matches the versioned prefix the public router mounts under, so it
// can be stripped before comparing.
//
// A pattern rather than the literal "/api/v1": the version is the one part of
// that path designed to change, and pinning it here would mean that moving the
// mount to v2 silently shuts the only way out of a one-time password — a
// consequence nobody would connect to this file.
var apiMount = regexp.MustCompile(`^/api/v[0-9]+`)

// identityKey is unexported so only this package can place an identity in a
// context — a handler cannot fabricate one.
type identityKey struct{}

// IdentityFrom returns the authenticated identity carried by ctx.
func IdentityFrom(ctx context.Context) (rbac.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(rbac.Identity)
	return id, ok
}

// MiddlewareConfig collects what the middleware needs.
type MiddlewareConfig struct {
	Sessions   *SessionStore
	Users      UserStore
	Authorizer *rbac.Authorizer
	Cookies    CookieWriter
	Logger     *slog.Logger
}

// Middleware turns a session cookie into an identity and enforces permissions.
type Middleware struct {
	sessions *SessionStore
	users    UserStore
	authz    *rbac.Authorizer
	cookies  CookieWriter
	log      *slog.Logger
}

// NewMiddleware assembles the authentication middleware.
func NewMiddleware(cfg MiddlewareConfig) *Middleware {
	return &Middleware{
		sessions: cfg.Sessions,
		users:    cfg.Users,
		authz:    cfg.Authorizer,
		cookies:  cfg.Cookies,
		log:      cfg.Logger,
	}
}

// Authenticate resolves the session cookie into an identity, or answers 401.
//
// The account is re-read on every request rather than trusted from the session
// record. That is one indexed query, and it is what makes blocking an account
// take effect immediately instead of whenever its session happens to lapse.
func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			m.unauthenticated(w, r)
			return
		}

		ctx := r.Context()
		session, err := m.sessions.Get(ctx, cookie.Value)
		if err != nil {
			if !errors.Is(err, ErrSessionNotFound) {
				m.log.ErrorContext(ctx, "could not read session", "error", err)
			}
			m.unauthenticated(w, r)
			return
		}

		user, err := m.users.ByID(ctx, session.UserID)
		if err != nil {
			if !errors.Is(err, users.ErrNotFound) {
				m.log.ErrorContext(ctx, "could not load the session's account", "error", err)
			}
			m.discard(ctx, cookie.Value)
			m.unauthenticated(w, r)
			return
		}

		// A blocked account and a session from before a "log out everywhere"
		// are both dead; drop the record so it stops occupying the store.
		if !user.IsActive() || session.Generation != user.SessionGeneration {
			m.discard(ctx, cookie.Value)
			m.unauthenticated(w, r)
			return
		}

		// An administrator-issued password is a handover secret, not a
		// credential to live on: until the user replaces it, the API is
		// closed except for the endpoints that let them do exactly that.
		// Enforcing it here, not in the UI, is what makes the flag real.
		if user.MustChangePassword && !passwordChangeExempt(r.URL.Path) {
			httpx.Error(w, r, http.StatusForbidden, CodePasswordChangeRequired,
				"Change your password before continuing")
			return
		}

		// Extend the session on activity, so working through a contest does not
		// end in being logged out mid-answer.
		if err := m.sessions.Refresh(ctx, cookie.Value); err != nil {
			m.log.WarnContext(ctx, "could not extend the session", "error", err)
		}

		identity := rbac.Identity{
			UserID:      user.ID,
			Login:       user.Login,
			Permissions: toSet(user.Permissions),
		}

		ctx = context.WithValue(ctx, identityKey{}, identity)
		ctx = logging.WithUserID(ctx, user.ID.String())

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission enforces an installation-wide permission.
func (m *Middleware) RequirePermission(permission string) func(http.Handler) http.Handler {
	return m.require(permission, func(*http.Request) (uuid.UUID, error) { return uuid.Nil, nil })
}

// RequireContestPermission enforces a permission scoped to the contest named in
// the URL. It must be mounted under a route carrying {contestID}.
func (m *Middleware) RequireContestPermission(permission string) func(http.Handler) http.Handler {
	return m.require(permission, func(r *http.Request) (uuid.UUID, error) {
		return uuid.Parse(chi.URLParam(r, contestIDParam))
	})
}

// require builds a permission gate over a scope extracted from the request.
func (m *Middleware) require(permission string, scope func(*http.Request) (uuid.UUID, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				// Reached without Authenticate, or by an anonymous caller.
				// 401 rather than 403: an anonymous request should not be told
				// that it merely lacks rights.
				m.unauthenticated(w, r)
				return
			}

			contestID, err := scope(r)
			if err != nil {
				httpx.Error(w, r, http.StatusBadRequest, CodeInvalidContestID, "Contest identifier is not valid")
				return
			}

			switch err := m.authz.Authorize(r.Context(), identity, permission, contestID); {
			case err == nil:
			case errors.Is(err, rbac.ErrForbidden):
				httpx.Error(w, r, http.StatusForbidden, CodeForbidden, "You do not have access to this resource")
				return
			default:
				m.log.ErrorContext(r.Context(), "authorisation check failed",
					"permission", permission, "error", err)
				httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// discard removes a session that turned out to be unusable.
func (m *Middleware) discard(ctx context.Context, token string) {
	if err := m.sessions.Delete(ctx, token); err != nil {
		m.log.WarnContext(ctx, "could not discard a dead session", "error", err)
	}
}

// unauthenticated answers 401 and clears the cookie, so a browser holding a
// dead session stops sending it.
func (m *Middleware) unauthenticated(w http.ResponseWriter, r *http.Request) {
	m.cookies.Clear(w)
	httpx.Error(w, r, http.StatusUnauthorized, CodeUnauthenticated, "Sign in to continue")
}

func passwordChangeExempt(path string) bool {
	// Trailing slashes are stripped so /auth/logout/ is the same door, not a
	// different one that happens to be shut.
	path = strings.TrimSuffix(apiMount.ReplaceAllString(path, ""), "/")

	_, exempt := passwordChangeExemptPaths[path]
	return exempt
}

func toSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}
