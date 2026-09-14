package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Brute-force limits. The per-account window stops guessing at one login; the
// per-address window stops a sweep across many logins from one machine.
const (
	maxLoginAttemptsPerAccount = 10
	loginAttemptWindow         = 15 * time.Minute

	// maxPasswordChangeAttempts caps how often a signed-in account may offer a
	// current password while changing it. The endpoint verifies a password
	// exactly as the login does, so without a ceiling somebody holding a
	// borrowed session could guess the current password at leisure and turn a
	// stolen browser tab into a permanent takeover.
	maxPasswordChangeAttempts = 10

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

// Login-failure reasons carried in the auth.login_failed audit payload.
//
// A closed set, not free text, for the same reason audit.Actions() is a
// closed set rather than whatever string a call site happens to pass: the
// trail is read by more people than the one investigating an incident, kept
// for a year, and the interface has to translate every reason into three
// languages — which only works against a fixed vocabulary.
//
// Exactly three, matching what the login endpoint itself ever distinguishes
// (architecture §7.2), and no finer: a wrong password and a login that does
// not exist are both ReasonInvalidCredentials, because the endpoint answers
// them identically and at the same time — recording which one happened would
// put in permanent storage a distinction the wire deliberately erases, and an
// administrator reading the trail would become the oracle the endpoint was
// built to deny everyone else. Both throttle windows (per address, per
// account — see checkThrottle) collapse into ReasonTooManyAttempts for the
// same reason: the caller is told "too many attempts" either way, never which
// counter tripped.
//
// These are vocabulary, not secrets: each is a code the audit trail stores and
// the interface renders in the reader's own language. A scanner reads
// "Credentials" in the name and asks whether a password was pasted into the
// source; the answer is that this names the *kind of refusal*, and nothing here
// is ever compared against anything a caller sends.
const (
	// #nosec G101 -- an audit reason code, not a credential.
	ReasonInvalidCredentials = "invalid_credentials"
	ReasonAccountBlocked     = "account_blocked"
	ReasonTooManyAttempts    = "too_many_attempts"
)

// ServiceConfig collects the service's collaborators.
type ServiceConfig struct {
	Users    UserStore
	Sessions *SessionStore
	Audit    *audit.Recorder
	Limiter  *Limiter
	Logger   *slog.Logger
	// Passwords is the process's one password hasher, shared with account
	// management. Its bound on concurrent hashing only holds if every caller
	// goes through the same one.
	Passwords *password.Hasher
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
	passwords     *password.Hasher
	log           *slog.Logger
	maxPerAddress int
}

// NewService assembles the authentication service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Passwords == nil {
		panic("auth: NewService needs the shared password hasher")
	}
	perAddress := cfg.MaxAttemptsPerAddress
	if perAddress <= 0 {
		perAddress = DefaultMaxLoginAttemptsPerAddress
	}
	return &Service{
		users:         cfg.Users,
		sessions:      cfg.Sessions,
		audit:         cfg.Audit,
		limiter:       cfg.Limiter,
		passwords:     cfg.Passwords,
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
//
// Besides the errors declared above, it returns password.ErrBusy when the
// process is already running as many password computations as it will: the
// attempt was counted but not evaluated.
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
	matched, verifyErr := s.passwords.Verify(ctx, hash, cmd.Password)
	if errors.Is(verifyErr, password.ErrBusy) {
		// No slot came free: the password was never evaluated, so this is
		// neither a failure to record nor a verdict to give. The attempt has
		// already been counted by checkThrottle, which is what keeps a refusal
		// from being free to retry. Both a known and an unknown login reach
		// this point the same way, so it says nothing about which one it was.
		return LoginResult{}, verifyErr
	}
	if verifyErr != nil {
		// A malformed stored digest is a data problem, not a reason to admit
		// anyone. Log it for operators and reject the attempt.
		s.log.ErrorContext(ctx, "stored password hash is unusable", "login", cmd.Login, "error", verifyErr)
		matched = false
	}

	if !found || !matched {
		s.recordFailure(ctx, cmd, ReasonInvalidCredentials)
		return LoginResult{}, ErrInvalidCredentials
	}

	// The password checked out, so the caller owns the account and may be told
	// why it is refused.
	if !user.IsActive() {
		s.recordFailure(ctx, cmd, ReasonAccountBlocked)
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
//
// The address is checked first, and the order is a defence rather than a
// style. Every subject the limiter sees becomes a key in the cache, and the
// account key is derived from whatever login the caller typed — an unbounded
// space. Checked the other way round, a single machine that had already spent
// its address budget could keep minting one new counter per invented login,
// and on the in-process cache that is a way to fill the store until every
// counter, and with it every sign-in, is refused. Spending the bounded key
// (one per address) before the unbounded one caps what a refused caller can
// create at its own address budget.
func (s *Service) checkThrottle(ctx context.Context, cmd LoginCommand) error {
	if cmd.IP != "" {
		allowed, err := s.limiter.Allow(ctx, "ip:"+cmd.IP, s.maxPerAddress, loginAttemptWindow)
		if err != nil {
			return err
		}
		if !allowed {
			s.recordFailure(ctx, cmd, ReasonTooManyAttempts)
			return ErrTooManyAttempts
		}
	}

	// accountSubject turns the login into a rate-limit cache key verbatim, and
	// nothing before this point has bounded it: the request body is capped at
	// a megabyte, not the login field inside it. No real account's login can
	// exceed users.MaxLoginLength, so a longer one is refused here, before it
	// can mint a key of its own size — the same guard CLAUDE.md's rule 5 asks
	// for, one step earlier. The answer is the ordinary "wrong login" one,
	// deliberately: this must not become a way to tell an existing login from
	// one that could never exist, and the address check above still applies,
	// so this is not a way around the per-address throttle either.
	if len(cmd.Login) > users.MaxLoginLength {
		return ErrInvalidCredentials
	}

	allowed, err := s.limiter.Allow(ctx, accountSubject(cmd.Login), maxLoginAttemptsPerAccount, loginAttemptWindow)
	if err != nil {
		return err
	}
	if !allowed {
		s.recordFailure(ctx, cmd, ReasonTooManyAttempts)
		return ErrTooManyAttempts
	}

	return nil
}

// AllowPasswordChange counts one attempt by the account to change its own
// password and reports ErrTooManyAttempts once the window is spent.
//
// It is keyed on the account rather than the address: the caller is already
// signed in, so the address is theirs either way, and what the ceiling guards
// against is a second person holding the same session. Every attempt counts,
// exactly as at sign-in; ClearPasswordChangeThrottle is what a success calls.
func (s *Service) AllowPasswordChange(ctx context.Context, userID uuid.UUID) error {
	allowed, err := s.limiter.Allow(ctx, passwordChangeSubject(userID), maxPasswordChangeAttempts, loginAttemptWindow)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrTooManyAttempts
	}
	return nil
}

// ClearPasswordChangeThrottle forgets the account's attempts after a
// successful change, so somebody who mistyped their current password twice and
// then got it right does not carry the count into the next window.
func (s *Service) ClearPasswordChangeThrottle(ctx context.Context, userID uuid.UUID) {
	if err := s.limiter.Reset(ctx, passwordChangeSubject(userID)); err != nil {
		s.log.WarnContext(ctx, "could not reset the password-change throttle", "error", err)
	}
}

// upgradeHash replaces a digest made with weaker parameters. A failure here
// costs nothing: the session is valid either way and the next login retries.
func (s *Service) upgradeHash(ctx context.Context, user users.User, plaintext string) {
	hash, err := s.passwords.Hash(ctx, plaintext)
	if err != nil {
		s.log.WarnContext(ctx, "could not re-hash password", "user_id", user.ID, "error", err)
		return
	}
	if err := s.users.SetPassword(ctx, user.ID, hash, user.MustChangePassword); err != nil {
		s.log.WarnContext(ctx, "could not store the upgraded password hash", "user_id", user.ID, "error", err)
	}
}

// recordFailure writes a failed attempt to the trail. The login is recorded
// (it is what an investigation searches by); the password never is. reason is
// always one of the Reason* constants above — every call site in this file
// names one, and that is what keeps the payload a closed vocabulary rather
// than whatever text a future call site might be tempted to pass.
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

func passwordChangeSubject(userID uuid.UUID) string { return "pwchange:" + userID.String() }

// mustHash builds dummyHash once at start-up, on a hasher of its own: it runs
// before any service exists, exactly once, and nothing else is hashing yet.
func mustHash(plaintext string) string {
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1})
	hash, err := hasher.Hash(context.Background(), plaintext)
	if err != nil {
		panic("auth: cannot build the timing-equalising hash: " + err.Error())
	}
	return hash
}
