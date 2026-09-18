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

// Authorizer decides whether an identity may exercise a permission, either
// installation-wide (uuid.Nil) or over one contest.
//
// The one method of *rbac.Authorizer this package uses, declared here by the
// consumer (Go layout rule 3) rather than the concrete type being held. It is
// what lets a test of the assembled router assert *which* permission a route
// demands rather than only that some check refused: contest.view and
// contest.edit are indistinguishable by outcome today (rbac's
// managerPermissions grants a manager both), so a 403 alone proves nothing
// about which of the two a route was mounted behind — and the reference
// answers in a contest package are exactly the thing that must be behind the
// stricter one.
type Authorizer interface {
	Authorize(ctx context.Context, id rbac.Identity, permission string, contestID uuid.UUID) error
}

// MiddlewareConfig collects what the middleware needs.
type MiddlewareConfig struct {
	Sessions *SessionStore
	Users    UserStore
	// Accounts, when set, spares the account read on requests that follow
	// one another within its lifetime. Nil reads the account every time.
	Accounts   *AccountCache
	Authorizer Authorizer
	Cookies    CookieWriter
	Logger     *slog.Logger
}

// Middleware turns a session cookie into an identity and enforces permissions.
type Middleware struct {
	sessions *SessionStore
	users    UserStore
	accounts *AccountCache
	authz    Authorizer
	cookies  CookieWriter
	log      *slog.Logger
}

// NewMiddleware assembles the authentication middleware.
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
//
// The account is looked up on every request rather than trusted from the
// session record, which is what makes blocking an account take effect
// immediately instead of whenever its session happens to lapse. With an
// AccountCache the lookup is usually a cache read; the cache is told about
// every change package users makes, and whatever it is not told about applies
// within its lifetime (see AccountCache). A refusal is never decided on a
// cached copy: the account is read from the database first.
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

		// A copy that would admit the request is used as it is. One that
		// would refuse it may be stale in the other direction — the account
		// signed in again after its sessions were retired, or was unblocked —
		// so the refusal is left to the database's answer.
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

		// A blocked account and a session from before a "log out everywhere"
		// are both dead; drop the record so it stops occupying the store.
		if !live(user, session) {
			m.discard(ctx, cookie.Value)
			m.unauthenticated(w, r)
			return
		}

		// Only on a miss, and only for an account this session may use: a
		// request served from the cache writes nothing. A copy that was
		// checked against the database is replaced only when it turned out
		// stale; one that was right to hold a one-time password at the door
		// is left alone, so knocking repeatedly does not write each time.
		if m.accounts != nil && !cached && (!hit || admits(user, session, r.URL.Path)) {
			m.accounts.store(ctx, slot, user)
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
		// end in being logged out mid-answer. From the record just read, and
		// only when it is due: the store decides whether a write is needed.
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

// SessionStillValid reports whether the session cookie on r would still be
// admitted by Authenticate: the session not signed out, not past its idle
// timeout or its maximum lifetime, and its account still allowed to use it —
// not blocked or deleted, its sessions not retired by a password change or a
// "log out everywhere", not held at the door by a one-time password. A request
// is authenticated once, when it arrives; a response held open for a long
// time — an event stream — asks again before each push so it does not outlive
// either the session or the account it was opened under.
//
// The account is answered the way Authenticate answers it: from the account
// cache when the copy there admits the request, from the database otherwise.
// An event stream asks far less often than the cache entry lives, so a quiet
// stream usually reads the database; the copy it stores is what the next
// ordinary request is answered from.
//
// An error means the session store or the account could not be read, which
// says nothing about either; the caller decides whether to wait and ask again.
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

// MayOnContest reports whether the request's identity holds the permission
// on the contest, by the same decision RequireContestPermission makes — for a
// response that tells the interface which doors to offer, not for a gate. An
// anonymous request may nothing; an error is a decision that could not be
// made, never a yes.
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

// live reports whether the account may still use the session: it is active,
// and the session is of its current generation.
func live(user users.User, session Session) bool {
	return user.IsActive() && session.Generation == user.SessionGeneration
}

// admits reports whether Authenticate would let the request through on this
// account: live, and not held at the door by a one-time password.
func admits(user users.User, session Session, path string) bool {
	return live(user, session) && (!user.MustChangePassword || passwordChangeExempt(path))
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
