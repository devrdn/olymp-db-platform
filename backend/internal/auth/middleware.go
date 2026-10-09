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

const SessionCookieName = "dbcontest_session"

const contestIDParam = "contestID"

// passwordChangeExemptPaths are what an account on its one-time password may
// reach: changing it, logging out, and reading itself. Exact paths after the
// mount prefix, not suffixes, so a later route ending the same way is not
// silently exempted.
var passwordChangeExemptPaths = map[string]struct{}{
	"/auth/password": {},
	"/auth/logout":   {},
	"/auth/me":       {},
}

// apiMount matches the versioned API prefix, as a pattern so a new version
// does not shut the way out of a one-time password.
var apiMount = regexp.MustCompile(`^/api/v[0-9]+`)

// identityKey is unexported so no handler can fabricate an identity.
type identityKey struct{}

func IdentityFrom(ctx context.Context) (rbac.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(rbac.Identity)
	return id, ok
}

// Authorizer is the method of *rbac.Authorizer this package uses (CLAUDE.md
// layout rule 3). As an interface it lets router tests assert which
// permission a route demands, since contest.view and contest.edit refuse
// alike.
type Authorizer interface {
	Authorize(ctx context.Context, id rbac.Identity, permission string, contestID uuid.UUID) error
}

type MiddlewareConfig struct {
	Sessions *SessionStore
	Users    UserStore
	// Accounts nil reads the account on every request.
	Accounts   *AccountCache
	Authorizer Authorizer
	Cookies    CookieWriter
	Logger     *slog.Logger
}

type Middleware struct {
	sessions *SessionStore
	users    UserStore
	accounts *AccountCache
	authz    Authorizer
	cookies  CookieWriter
	log      *slog.Logger
}

func NewMiddleware(cfg MiddlewareConfig) *Middleware {
	return &Middleware{
		sessions: cfg.Sessions,
		users:    cfg.Users,
		accounts: cfg.Accounts,
		authz:    cfg.Authorizer,
		cookies:  cfg.Cookies,
		log:      cfg.Logger,
	}
}

// Authenticate resolves the session cookie into an identity, or answers 401.
// The account is checked on every request, so a block takes effect at once;
// with an AccountCache that is usually a cache read, but a refusal is always
// confirmed against the database.
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

		// A cached copy that would refuse may be stale, so the database
		// decides refusals.
		var (
			user   users.User
			hit    bool
			cached bool
			slot   accountSlot
		)
		if m.accounts != nil {
			user, hit, slot = m.accounts.lookup(ctx, session.UserID)
			cached = hit && admits(user, session, r.URL.Path)
		}
		if !cached {
			user, err = m.users.ByID(ctx, session.UserID)
			if err != nil {
				if !errors.Is(err, users.ErrNotFound) {
					m.log.ErrorContext(ctx, "could not load the session's account", "error", err)
				}
				m.discard(ctx, cookie.Value)
				m.unauthenticated(w, r)
				return
			}
		}

		// Drop a dead session's record from the store.
		if !live(user, session) {
			m.discard(ctx, cookie.Value)
			m.unauthenticated(w, r)
			return
		}

		// Store only on a miss, or when a checked copy proved stale, so
		// requests served from the cache, or repeated refusals, write
		// nothing (CLAUDE.md rule 6).
		if m.accounts != nil && !cached && (!hit || admits(user, session, r.URL.Path)) {
			m.accounts.store(ctx, slot, user)
		}

		// Until a one-time password is replaced, only the exempt paths
		// answer; enforced here, not in the UI.
		if user.MustChangePassword && !passwordChangeExempt(r.URL.Path) {
			httpx.Error(w, r, http.StatusForbidden, CodePasswordChangeRequired,
				"Change your password before continuing")
			return
		}

		// Extend the session on activity; the store writes only when due
		// (CLAUDE.md rule 6).
		if err := m.sessions.Touch(ctx, cookie.Value, session); err != nil {
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

// SessionStillValid reports whether Authenticate would still admit the
// session cookie on r. A long-held response such as an event stream asks
// before each push, so it does not outlive the session or the account. The
// account is checked as Authenticate checks it. An error says nothing about
// validity; the caller decides whether to ask again.
func (m *Middleware) SessionStillValid(r *http.Request) (bool, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return false, nil
	}
	ctx := r.Context()
	session, err := m.sessions.Get(ctx, cookie.Value)
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return false, nil
	case err != nil:
		return false, err
	}

	var (
		user users.User
		hit  bool
		slot accountSlot
	)
	if m.accounts != nil {
		user, hit, slot = m.accounts.lookup(ctx, session.UserID)
	}
	if hit && admits(user, session, r.URL.Path) {
		return true, nil
	}
	user, err = m.users.ByID(ctx, session.UserID)
	switch {
	case errors.Is(err, users.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	if !admits(user, session, r.URL.Path) {
		return false, nil
	}
	if m.accounts != nil {
		m.accounts.store(ctx, slot, user)
	}
	return true, nil
}

func (m *Middleware) RequirePermission(permission string) func(http.Handler) http.Handler {
	return m.require(permission, func(*http.Request) (uuid.UUID, error) { return uuid.Nil, nil })
}

// RequireContestPermission enforces a permission on the URL's {contestID};
// mount it only under a route that carries one.
func (m *Middleware) RequireContestPermission(permission string) func(http.Handler) http.Handler {
	return m.require(permission, func(r *http.Request) (uuid.UUID, error) {
		return uuid.Parse(chi.URLParam(r, contestIDParam))
	})
}

// MayOnContest makes RequireContestPermission's decision for a response that
// tells the interface which actions to offer, not as a gate. An error is
// never a yes.
func (m *Middleware) MayOnContest(r *http.Request, permission string, contestID uuid.UUID) (bool, error) {
	identity, ok := IdentityFrom(r.Context())
	if !ok {
		return false, nil
	}
	switch err := m.authz.Authorize(r.Context(), identity, permission, contestID); {
	case err == nil:
		return true, nil
	case errors.Is(err, rbac.ErrForbidden):
		return false, nil
	default:
		return false, err
	}
}

func (m *Middleware) require(permission string, scope func(*http.Request) (uuid.UUID, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := IdentityFrom(r.Context())
			if !ok {
				// Anonymous: 401, not 403.
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

func (m *Middleware) discard(ctx context.Context, token string) {
	if err := m.sessions.Delete(ctx, token); err != nil {
		m.log.WarnContext(ctx, "could not discard a dead session", "error", err)
	}
}

// unauthenticated answers 401 and clears the cookie.
func (m *Middleware) unauthenticated(w http.ResponseWriter, r *http.Request) {
	m.cookies.Clear(w)
	httpx.Error(w, r, http.StatusUnauthorized, CodeUnauthenticated, "Sign in to continue")
}

func live(user users.User, session Session) bool {
	return user.IsActive() && session.Generation == user.SessionGeneration
}

func admits(user users.User, session Session, path string) bool {
	return live(user, session) && (!user.MustChangePassword || passwordChangeExempt(path))
}

func passwordChangeExempt(path string) bool {
	// A trailing slash names the same path.
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
