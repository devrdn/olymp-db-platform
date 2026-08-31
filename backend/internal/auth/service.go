package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// Brute-force limits. The per-account window stops guessing at one login; the
// per-address window stops a sweep across many logins from one machine.
const (
	maxLoginAttemptsPerAccount = 10
	loginAttemptWindow         = 15 * time.Minute

	// DefaultMaxLoginAttemptsPerAddress is the per-address ceiling when the
	// deployment does not state one.
	//
	// It counts successful sign-ins too, and must: resetting it on success
	// would let an attacker launder their own counter by signing in to an
	// account they already hold. That makes it a ceiling on people, not only
	// on guesses — a lecture hall behind one NAT address is one address here,
	// and thirty was low enough to refuse honest students at the start of an
	// olympiad. The per-account limit of ten is what actually stops guessing;
	// this one exists to stop a sweep across many logins, and a few hundred
	// is still far below what a sweep needs.
	DefaultMaxLoginAttemptsPerAddress = 300
)

// dummyHash is verified against when the login does not exist, so a missing
// account costs the same time as a wrong password. Its plaintext is unknown
// and irrelevant — only the work it forces matters.
var dummyHash = mustHash("a password nobody uses to keep the timing honest")

// Errors returned to the login endpoint.
var (
	// ErrInvalidCredentials covers both a wrong password and a login that does
	// not exist. The two must be indistinguishable, or the form becomes a
	// directory of who has an account.
	ErrInvalidCredentials = errors.New("invalid login or password")
	// ErrAccountBlocked is reported only after the password checked out: the
	// owner deserves to know, a guesser must learn nothing.
	ErrAccountBlocked = errors.New("account is blocked")
	// ErrTooManyAttempts reports throttling.
	ErrTooManyAttempts = errors.New("too many login attempts")
)

// ServiceConfig collects the service's collaborators.
type ServiceConfig struct {
	Users    UserStore
	Sessions *SessionStore
	Audit    *audit.Recorder
	Limiter  *Limiter
	Logger   *slog.Logger
	// MaxAttemptsPerAddress caps sign-in attempts from one address in a
	// window. Zero takes DefaultMaxLoginAttemptsPerAddress; a site whose
	// participants share one NAT address raises it.
	MaxAttemptsPerAddress int
}

// Service runs the login and logout flows.
type Service struct {
	users         UserStore
	sessions      *SessionStore
	audit         *audit.Recorder
	limiter       *Limiter
	log           *slog.Logger
	maxPerAddress int
}

// NewService assembles the authentication service.
func NewService(cfg ServiceConfig) *Service {
	perAddress := cfg.MaxAttemptsPerAddress
	if perAddress <= 0 {
		perAddress = DefaultMaxLoginAttemptsPerAddress
	}
	return &Service{
		users:         cfg.Users,
		sessions:      cfg.Sessions,
		audit:         cfg.Audit,
		limiter:       cfg.Limiter,
		log:           cfg.Logger,
		maxPerAddress: perAddress,
	}
}

// Sessions exposes the session store to the middleware.
func (s *Service) Sessions() *SessionStore { return s.sessions }

// LoginCommand is one authentication attempt.
type LoginCommand struct {
	Login     string
	Password  string
	IP        string
	UserAgent string
}

// LoginResult is what the handler needs to answer a successful attempt.
type LoginResult struct {
	Token              string
	User               users.User
	MustChangePassword bool
}

// Login authenticates a set of credentials and issues a session.
//
// The order of the steps is deliberate: throttling first, then a password
// comparison that always runs, then the account's state. Anything that returns
// earlier for one kind of failure than another becomes an oracle.
func (s *Service) Login(ctx context.Context, cmd LoginCommand) (LoginResult, error) {
	if err := s.checkThrottle(ctx, cmd); err != nil {
		return LoginResult{}, err
	}

	user, lookupErr := s.users.ByLogin(ctx, cmd.Login)
	if lookupErr != nil && !errors.Is(lookupErr, users.ErrNotFound) {
		return LoginResult{}, lookupErr
	}
	found := lookupErr == nil

	// Verify even when there is no such account: skipping the comparison would
	// answer in microseconds instead of tens of milliseconds and let an
	// attacker enumerate logins by timing alone.
	hash := dummyHash
	if found {
		hash = user.PasswordHash
	}
	matched, verifyErr := password.Verify(hash, cmd.Password)
	if verifyErr != nil {
		// A malformed stored digest is a data problem, not a reason to admit
		// anyone. Log it for operators and reject the attempt.
		s.log.ErrorContext(ctx, "stored password hash is unusable", "login", cmd.Login, "error", verifyErr)
		matched = false
	}

	if !found || !matched {
		s.recordFailure(ctx, cmd, "invalid_credentials")
		return LoginResult{}, ErrInvalidCredentials
	}

	// The password checked out, so the caller owns the account and may be told
	// why it is refused.
	if !user.IsActive() {
		s.recordFailure(ctx, cmd, "account_blocked")
		return LoginResult{}, ErrAccountBlocked
	}

	if password.NeedsRehash(user.PasswordHash) {
		s.upgradeHash(ctx, user, cmd.Password)
	}

	token, err := s.sessions.Create(ctx, Principal{
		UserID:     user.ID,
		Login:      user.Login,
		Generation: user.SessionGeneration,
	})
	if err != nil {
		return LoginResult{}, err
	}

	now := time.Now().UTC()
	if err := s.users.RecordLogin(ctx, user.ID, now); err != nil {
		// The session is already valid; failing the login over a bookkeeping
		// write would be worse than the missing timestamp.
		s.log.WarnContext(ctx, "could not stamp last login", "user_id", user.ID, "error", err)
	}

	// A successful login clears the counter, so someone who mistypes twice and
	// then succeeds does not stay near the limit.
	if err := s.limiter.Reset(ctx, accountSubject(cmd.Login)); err != nil {
		s.log.WarnContext(ctx, "could not reset the login throttle", "error", err)
	}

	s.record(ctx, audit.Entry{
		ActorID:   &user.ID,
		Action:    audit.ActionAuthLogin,
		Entity:    "user",
		EntityID:  user.ID.String(),
		IP:        cmd.IP,
		UserAgent: cmd.UserAgent,
	})

	return LoginResult{
		Token:              token,
		User:               user,
		MustChangePassword: user.MustChangePassword,
	}, nil
}

// Logout ends a session. An unknown token is not an error: a stale cookie is a
// normal thing for a browser to send.
func (s *Service) Logout(ctx context.Context, token string) error {
	session, err := s.sessions.Get(ctx, token)
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return nil
	case err != nil:
		return err
	}

	if err := s.sessions.Delete(ctx, token); err != nil {
		return err
	}

	s.record(ctx, audit.Entry{
		ActorID:  &session.UserID,
		Action:   audit.ActionAuthLogout,
		Entity:   "user",
		EntityID: session.UserID.String(),
	})
	return nil
}

// checkThrottle applies both windows. It runs before anything else, so a
// throttled attempt costs no hashing work either.
func (s *Service) checkThrottle(ctx context.Context, cmd LoginCommand) error {
	allowed, err := s.limiter.Allow(ctx, accountSubject(cmd.Login), maxLoginAttemptsPerAccount, loginAttemptWindow)
	if err != nil {
		return err
	}
	if !allowed {
		s.recordFailure(ctx, cmd, "throttled_account")
		return ErrTooManyAttempts
	}

	if cmd.IP != "" {
		allowed, err = s.limiter.Allow(ctx, "ip:"+cmd.IP, s.maxPerAddress, loginAttemptWindow)
		if err != nil {
			return err
		}
		if !allowed {
			s.recordFailure(ctx, cmd, "throttled_address")
			return ErrTooManyAttempts
		}
	}

	return nil
}

// upgradeHash replaces a digest made with weaker parameters. A failure here
// costs nothing: the session is valid either way and the next login retries.
func (s *Service) upgradeHash(ctx context.Context, user users.User, plaintext string) {
	hash, err := password.Hash(plaintext)
	if err != nil {
		s.log.WarnContext(ctx, "could not re-hash password", "user_id", user.ID, "error", err)
		return
	}
	if err := s.users.SetPassword(ctx, user.ID, hash, user.MustChangePassword); err != nil {
		s.log.WarnContext(ctx, "could not store the upgraded password hash", "user_id", user.ID, "error", err)
	}
}

// recordFailure writes a failed attempt to the trail. The login is recorded
// (it is what an investigation searches by); the password never is.
func (s *Service) recordFailure(ctx context.Context, cmd LoginCommand, reason string) {
	s.record(ctx, audit.Entry{
		Action:    audit.ActionAuthLoginFailed,
		Entity:    "user",
		Payload:   map[string]any{"login": cmd.Login, "reason": reason},
		IP:        cmd.IP,
		UserAgent: cmd.UserAgent,
	})
}

// record writes an audit entry, logging rather than failing the request when
// the trail is unavailable: refusing a login because the journal is down would
// turn an observability outage into an authentication outage.
func (s *Service) record(ctx context.Context, entry audit.Entry) {
	if err := s.audit.Record(ctx, entry); err != nil {
		s.log.ErrorContext(ctx, "could not write audit entry", "action", entry.Action, "error", err)
	}
}

func accountSubject(login string) string { return "login:" + normalizeLogin(login) }

func mustHash(plaintext string) string {
	hash, err := password.Hash(plaintext)
	if err != nil {
		panic("auth: cannot build the timing-equalising hash: " + err.Error())
	}
	return hash
}
