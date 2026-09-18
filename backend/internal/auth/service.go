package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Brute-force limits, all over the same fixed window. The per-address window
// stops a sweep across many logins from one machine; the account-and-address
// window stops guessing at one login from one machine; the account-wide
// ceiling stops a guess spread across many machines.
const (
	// maxLoginAttemptsPerAccountAddress is the guessing limit: attempts at
	// one account from one address.
	//
	// It used to be keyed on the account alone, and that made it a lockout
	// anybody could apply: logins are not secret (a public leaderboard may
	// list them), and ten wrong guesses from anywhere kept the owner out for
	// the rest of the window, correct password and all. Keyed on the pair,
	// the guesser is still stopped, and the owner at their own address is
	// not.
	maxLoginAttemptsPerAccountAddress = 10
	loginAttemptWindow                = 15 * time.Minute

	// DefaultMaxLoginAttemptsPerAccount is the account-wide ceiling when the
	// deployment does not state one: attempts at one account from every
	// address together.
	//
	// Keying the guessing limit on the address gives somebody with many
	// addresses a fresh budget at each, and this is the backstop against
	// that. It is ten times the per-address limit, so reaching it takes at
	// least ten addresses each spending their whole budget — an attempt
	// refused at its own address never reaches this counter — and one person
	// mistyping never comes near it. The price, accepted knowingly, is that a
	// guess distributed that widely can still hold the account shut for the
	// window.
	DefaultMaxLoginAttemptsPerAccount = 100

	// DefaultMaxLoginAttemptsPerDevice is the guessing limit for a browser
	// the owner has signed in from before, when the deployment does not
	// state one. Such a browser skips the address and account limits (see
	// Login), so a copied device cookie still carries a limit of its own —
	// the same ten a stranger gets at one address.
	DefaultMaxLoginAttemptsPerDevice = 10

	// DefaultMaxTrustedLoginAttemptsPerAccount caps the attempts through every
	// trusted browser of one account together, when the deployment does not
	// state one. A per-device limit alone multiplies by however many cookies
	// were collected by signing in again and again; this does not. Only a
	// holder of a valid cookie for the account can spend it, so it is not a
	// lever for a rival — and twenty a quarter hour is far beyond one person
	// signing in from their own browsers.
	DefaultMaxTrustedLoginAttemptsPerAccount = 20

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

	// waitingPerSlot sizes each address's share of the queue for a hashing
	// slot, per slot the hasher has. A verification takes roughly 75-100 ms, so
	// at the compose default of 4 slots the 32 attempts one address may queue
	// drain in well under a second, leaving a sign-in from anywhere else behind
	// them served within the 2 s wait, while a lecture hall signing in through
	// one NAT address at the start of a round mostly queues instead of being
	// refused.
	waitingPerSlot = 8
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
// built to deny everyone else. Every throttle window (per address, per
// account and address, per account — see checkThrottle) collapses into
// ReasonTooManyAttempts for the same reason: the caller is told "too many
// attempts" either way, never which counter tripped.
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
	// MaxAttemptsPerAccount caps sign-in attempts at one account from every
	// address together in a window. Zero takes
	// DefaultMaxLoginAttemptsPerAccount.
	MaxAttemptsPerAccount int
	// Devices issues and checks the cookie that marks a browser the account's
	// owner has signed in from.
	Devices *DeviceTrust
	// MaxAttemptsPerDevice caps sign-in attempts through one trusted browser
	// in a window. Zero takes DefaultMaxLoginAttemptsPerDevice.
	MaxAttemptsPerDevice int
	// MaxTrustedAttemptsPerAccount caps sign-in attempts through every trusted
	// browser of one account together in a window. Zero takes
	// DefaultMaxTrustedLoginAttemptsPerAccount.
	MaxTrustedAttemptsPerAccount int
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
	maxPerAccount int
	devices       *DeviceTrust
	maxPerDevice  int
	maxTrusted    int
	// waiting caps each address's share of the queue for a hashing slot.
	waiting *waitingQueue
}

// NewService assembles the authentication service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Passwords == nil {
		panic("auth: NewService needs the shared password hasher")
	}
	if cfg.Devices == nil {
		panic("auth: NewService needs device trust")
	}
	perDevice := cfg.MaxAttemptsPerDevice
	if perDevice <= 0 {
		perDevice = DefaultMaxLoginAttemptsPerDevice
	}
	trustedPerAccount := cfg.MaxTrustedAttemptsPerAccount
	if trustedPerAccount <= 0 {
		trustedPerAccount = DefaultMaxTrustedLoginAttemptsPerAccount
	}
	perAddress := cfg.MaxAttemptsPerAddress
	if perAddress <= 0 {
		perAddress = DefaultMaxLoginAttemptsPerAddress
	}
	perAccount := cfg.MaxAttemptsPerAccount
	if perAccount <= 0 {
		perAccount = DefaultMaxLoginAttemptsPerAccount
	}
	return &Service{
		users:         cfg.Users,
		sessions:      cfg.Sessions,
		audit:         cfg.Audit,
		limiter:       cfg.Limiter,
		passwords:     cfg.Passwords,
		log:           cfg.Logger,
		maxPerAddress: perAddress,
		maxPerAccount: perAccount,
		devices:       cfg.Devices,
		maxPerDevice:  perDevice,
		maxTrusted:    trustedPerAccount,
		waiting:       newWaitingQueue(waitingPerSlot * cfg.Passwords.Concurrency()),
	}
}

// DeviceCookieLifetime is how long the device cookie issued at sign-in lives.
func (s *Service) DeviceCookieLifetime() time.Duration { return s.devices.TTL() }

// Sessions exposes the session store to the middleware.
func (s *Service) Sessions() *SessionStore { return s.sessions }

// LoginCommand is one authentication attempt.
type LoginCommand struct {
	Login     string
	Password  string
	IP        string
	UserAgent string
	// DeviceToken is the device cookie the browser sent, if any.
	DeviceToken string
	// PreviousToken is the session cookie the browser sent, if any: a browser
	// signing in again while signed in. A successful sign-in ends that
	// session, so it is replaced rather than left alive beside the new one.
	PreviousToken string
}

// LoginResult is what the handler needs to answer a successful attempt.
type LoginResult struct {
	Token              string
	User               users.User
	MustChangePassword bool
	// DeviceToken is the device cookie to hand the browser.
	DeviceToken string
}

// Login authenticates a set of credentials and issues a session.
//
// The order of the steps is deliberate, and each one is explained where it
// happens:
//
//  1. The budget the attempt spends before anything else: through a trusted
//     browser, that browser's limit and the account's trusted budget
//     (spendTrusted); otherwise — or once those are spent — the address budget
//     and the length guards.
//  2. The account lookup, which runs for every attempt the same way, whether
//     or not the login exists.
//  3. Whether the device cookie still vouches for the account; if it does
//     not, the address budget it skipped.
//  4. The hashing slot, then the account's guessing limit and ceiling for an
//     attempt without trust. The slot comes first so a refusal for load costs
//     the account nothing; it is taken only after the lookup, and released
//     before anything is written, so it is held across the counters and the
//     computation and no longer.
//  5. The comparison, which always runs, then the account's state.
//
// Anything that returns earlier for one kind of failure than another becomes
// an oracle.
//
// Besides the errors declared above, it returns password.ErrBusy when the
// process is already running as many password computations as it will: the
// attempt spent its step-1 budget but was not evaluated, and cost the
// account's guessing limit and ceiling nothing.
func (s *Service) Login(ctx context.Context, cmd LoginCommand) (LoginResult, error) {
	// A browser the owner has signed in from before carries a device cookie,
	// and an attempt through it is not limited by the address, which a lecture
	// hall shares with every rival in it, nor by the account's guessing limit
	// and ceiling, which anybody who knows the login can spend. That is what
	// keeps a rival at the next desk from locking the owner out. It spends
	// instead what only the cookie's holders can spend (spendTrusted).
	//
	// The cookie is only looked at when the lengths are within bounds: its
	// check hashes the login, and an oversized one must go the ordinary way,
	// through the address budget, to its refusal.
	var (
		device  Device
		trusted bool
	)
	if withinLengths(cmd) {
		device, trusted = s.devices.Verify(cmd.DeviceToken, cmd.Login)
	}

	if trusted {
		spent, err := s.spendTrusted(ctx, cmd, device)
		if err != nil {
			return LoginResult{}, err
		}
		// Past the trusted limits the attempt is not refused: it goes on as
		// one with no cookie at all, through the ordinary limits below. A
		// refusal here would let whoever holds a copy of the cookie spend the
		// owner's trusted limits and shut the owner out — the very lockout
		// the cookie is for. Nothing is recorded for it either: nothing was
		// refused, and the ordinary path records its own refusals.
		trusted = !spent
	}
	if !trusted {
		if err := s.checkAddress(ctx, cmd); err != nil {
			return LoginResult{}, err
		}
	}

	user, lookupErr := s.users.ByLogin(ctx, cmd.Login)
	if lookupErr != nil && !errors.Is(lookupErr, users.ErrNotFound) {
		return LoginResult{}, lookupErr
	}
	found := lookupErr == nil

	// The cookie was issued for this login, but the account may have moved on
	// since: a password change, a block, a deletion, the login taken by a new
	// account. Then it vouches for nothing, and the attempt pays the ordinary
	// limits it skipped — exactly as if it had come with no cookie at all.
	if trusted && !(found && device.Vouches(user)) {
		trusted = false
		if err := s.checkAddress(ctx, cmd); err != nil {
			return LoginResult{}, err
		}
	}

	// The account's generation is read before the slot: it is a lookup of
	// its own, and a slot held across it would stand idle.
	var gen string
	if !trusted {
		var err error
		if gen, err = s.limiter.Generation(ctx, accountFamily(cmd.Login)); err != nil {
			return LoginResult{}, err
		}
	}

	// No slot came free: the password was never evaluated, so this is neither
	// a failure to record nor a verdict to give. It is reached by a known and
	// an unknown login alike, after the same lookup, so it says nothing about
	// which one it was.
	slot, err := s.holdSlot(ctx, cmd, trusted)
	if err != nil {
		return LoginResult{}, err
	}
	defer slot.Release()

	if !trusted {
		refused, err := s.spendAccount(ctx, cmd, gen)
		if err != nil {
			return LoginResult{}, err
		}
		if refused {
			// The refusal is recorded after the slot is given back, not
			// while a sign-in behind this one waits for it.
			slot.Release()
			s.recordFailure(ctx, cmd, ReasonTooManyAttempts)
			return LoginResult{}, ErrTooManyAttempts
		}
	}

	// Verify even when there is no such account: skipping the comparison would
	// answer in microseconds instead of tens of milliseconds and let an
	// attacker enumerate logins by timing alone.
	hash := dummyHash
	if found {
		hash = user.PasswordHash
	}
	matched, verifyErr := slot.Verify(hash, cmd.Password)
	// The comparison is done. Everything after it is a write, and the rehash
	// below takes a slot of its own.
	slot.Release()
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

	// The browser's previous session, if it sent one, ends now that it has a
	// new one: left alive it would be an orphan nobody holds, and to the
	// monitoring trail a second device. Only after the new session exists,
	// so a sign-in that fails signs nobody out. Best-effort: a cookie naming
	// no session is nothing to end, and a store that cannot delete it leaves
	// it to expire on its own.
	if cmd.PreviousToken != "" && cmd.PreviousToken != token {
		if err := s.sessions.Delete(ctx, cmd.PreviousToken); err != nil {
			s.log.WarnContext(ctx, "could not end the replaced session", "error", err)
		}
	}

	now := time.Now().UTC()
	if err := s.users.RecordLogin(ctx, user.ID, now); err != nil {
		// The session is already valid; failing the login over a bookkeeping
		// write would be worse than the missing timestamp.
		s.log.WarnContext(ctx, "could not stamp last login", "user_id", user.ID, "error", err)
	}

	var deviceToken string
	if trusted && s.devices.DueForRenewal(device) {
		// Past half its lifetime the cookie is renewed under the same device
		// id, so a browser in regular use keeps its trust without an ordinary
		// sign-in every month. Renewal happens only here, after the right
		// password: a copy of the cookie without the password never extends
		// itself. The same id keeps every attempt already counted against the
		// device, and nothing is reset.
		if deviceToken, err = s.devices.Issue(user, device.ID); err != nil {
			s.log.WarnContext(ctx, "could not renew a device cookie", "error", err)
			deviceToken = ""
		}
	}
	if !trusted {
		// A successful ordinary sign-in clears the guessing counter for this
		// address, so someone who mistypes twice and then succeeds does not
		// stay near the limit. The account-wide ceiling is left to its
		// window: a success here says nothing about attempts made from
		// elsewhere, and clearing it would hand a distributed guess a fresh
		// budget every time the owner signs in.
		if err := s.resetAccountAddress(ctx, cmd, gen); err != nil {
			s.log.WarnContext(ctx, "could not reset the login throttle", "error", err)
		}

		// This browser has now signed in to the account, so it gets a cookie
		// saying so. A failure costs only the trust: the session is valid
		// either way.
		if deviceToken, err = s.devices.Issue(user, DeviceID{}); err != nil {
			s.log.WarnContext(ctx, "could not issue a device cookie", "error", err)
			deviceToken = ""
		}
	}
	// A trusted success resets nothing. The device's limit and the account's
	// trusted budget count every attempt, successes included, exactly as the
	// address budget does: resetting them would let the account's own cookie
	// sign in without limit on the trusted path, each time taking a hashing
	// slot, a session and an audit row. Past them the attempt pays the
	// ordinary limits, so the bound holds whichever path it takes.

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
		DeviceToken:        deviceToken,
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

// Sign-in throttling, in the order Login spends it. Every counter reached is
// spent even when a later step refuses, which is what keeps a refusal from
// being free to retry.
//
// Without a device cookie that vouches for the account:
//
//  1. checkAddress: the address budget. Every subject the limiter sees becomes
//     a key in the cache, and the account keys are derived from whatever login
//     the caller typed — an unbounded space. Checked the other way round, a
//     machine that had already spent its address budget could keep minting
//     one new counter per invented login, and on the in-process cache that is
//     a way to fill the store until every counter, and with it every sign-in,
//     is refused. Spending the bounded key (one per address) first caps what a
//     refused caller can create at its own address budget.
//  2. checkAddress: the lengths, before either value becomes a key or reaches
//     a hash.
//  3. spendAccount, once the hashing slot is held: the account from this
//     address — the guessing limit — and then the account from every address
//     — the ceiling. Only an attempt the guessing limit let through reaches
//     the ceiling, so one address repeating refused attempts cannot climb to
//     it and shut the owner out everywhere.
//
// Through a trusted browser, spendTrusted replaces all three and runs first.
// It needs no address-first guard: its keys are a device id and a login taken
// from a cookie this installation signed, which a caller cannot invent. Once
// its limits are spent, or when a cookie verifies but no longer vouches for
// the account, the attempt falls back to all three.

// withinLengths reports whether the login and password are short enough to be
// real. No account's login is longer than users.MaxLoginLength, and no stored
// digest is of a password longer than password.MaxLength.
func withinLengths(cmd LoginCommand) bool {
	return len(cmd.Login) <= users.MaxLoginLength && len(cmd.Password) <= password.MaxLength
}

// spendTrusted spends a trusted browser's own limit and then the account's
// trusted budget, both under the account's throttle generation so a staff
// unlock clears them too, and reports whether either was already spent. It
// records nothing: a spent trusted limit is not a refusal (see Login).
//
// Both count every attempt, successes included, and nothing resets them but
// their window or an unlock. The device limit bounds one copied cookie; the
// trusted budget bounds all of the account's cookies together, however many
// were collected by signing in again. Past either, the attempt pays the
// ordinary limits. The trusted budget is not spent by an attempt the device
// limit already turned away, so one copied cookie cannot use up the budget
// the owner's other browsers share.
func (s *Service) spendTrusted(ctx context.Context, cmd LoginCommand, device Device) (bool, error) {
	gen, err := s.limiter.Generation(ctx, accountFamily(cmd.Login))
	if err != nil {
		return false, err
	}
	for _, window := range []struct {
		subject string
		limit   int
	}{
		{deviceSubject(device.ID, gen), s.maxPerDevice},
		{trustedSubject(cmd.Login, gen), s.maxTrusted},
	} {
		allowed, err := s.limiter.Allow(ctx, window.subject, window.limit, loginAttemptWindow)
		if err != nil {
			return false, err
		}
		if !allowed {
			return true, nil
		}
	}
	return false, nil
}

// checkAddress spends the address budget and applies the length guards.
func (s *Service) checkAddress(ctx context.Context, cmd LoginCommand) error {
	if cmd.IP != "" {
		allowed, err := s.limiter.Allow(ctx, "ip:"+httpx.AddressSubject(cmd.IP), s.maxPerAddress, loginAttemptWindow)
		if err != nil {
			return err
		}
		if !allowed {
			s.recordFailure(ctx, cmd, ReasonTooManyAttempts)
			return ErrTooManyAttempts
		}
	}

	// The account keys hold the login verbatim, and nothing before this point
	// has bounded it: the request body is capped at a megabyte, not the login
	// field inside it. No real account's login can exceed
	// users.MaxLoginLength, so a longer one is refused here, before it can
	// mint a key of its own size. The answer is the ordinary "wrong login"
	// one, deliberately: this must not become a way to tell an existing login
	// from one that could never exist, and the address check above still
	// applies, so this is not a way around the per-address throttle either.
	//
	// The password gets the same treatment for a different reason: no stored
	// digest can be of one longer than password.MaxLength, so the answer is
	// already known, and neither a counter nor a hashing slot is spent on it.
	if !withinLengths(cmd) {
		return ErrInvalidCredentials
	}
	return nil
}

// errAddressQueueFull refuses an attempt whose address already has its share
// of the queue for a hashing slot. It is password.ErrBusy to every caller: the
// password was not evaluated, and the answer is the same busy one.
var errAddressQueueFull = fmt.Errorf("%w: this address already has its share of sign-ins waiting", password.ErrBusy)

// holdSlot takes a hashing slot for the attempt.
//
// An attempt without trust first joins its address's share of the queue, and
// past that share it is refused without waiting: its address budget is already
// spent, so the refusal is counted as every other one is. An attempt through a
// browser that still vouches for the account skips the share, as it skips the
// address budget, so a rival flooding from the same lecture hall cannot keep
// the owner's own browser out; the trusted budgets bound how many of those
// there can be.
func (s *Service) holdSlot(ctx context.Context, cmd LoginCommand, trusted bool) (*password.Slot, error) {
	if trusted || cmd.IP == "" {
		return s.passwords.Hold(ctx)
	}
	leave, ok := s.waiting.join(httpx.AddressSubject(cmd.IP))
	if !ok {
		return nil, errAddressQueueFull
	}
	// Only waiting is capped: once the slot is taken or refused the attempt
	// has left the queue.
	defer leave()
	return s.passwords.Hold(ctx)
}

// spendAccount spends the account's guessing limit and then its ceiling under
// generation gen, and reports whether either refused. It records nothing: the
// caller holds the hashing slot and records the refusal once it has let go.
func (s *Service) spendAccount(ctx context.Context, cmd LoginCommand, gen string) (bool, error) {
	for _, window := range []struct {
		subject string
		limit   int
	}{
		{accountAddressSubject(cmd.Login, cmd.IP, gen), maxLoginAttemptsPerAccountAddress},
		{accountSubject(cmd.Login, gen), s.maxPerAccount},
	} {
		allowed, err := s.limiter.Allow(ctx, window.subject, window.limit, loginAttemptWindow)
		if err != nil {
			return false, err
		}
		if !allowed {
			return true, nil
		}
	}
	return false, nil
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

// UnlockSignIn clears the sign-in throttling of an account: its guessing
// limit at every address and its account-wide ceiling. The address budgets are
// left alone — they are about machines, and clearing one for an account's sake
// would reopen a sweep across every other login.
//
// The counters are keyed by the login and by whatever address each attempt
// came from, so there is no list of them to delete. Every account key carries
// the account's throttle generation instead (see accountGeneration), and a new
// generation abandons them all.
//
// The unlock is made before it is recorded, and a failure to record it is
// returned rather than logged: this is an administrator's action, and one the
// trail cannot show should not look like a success to whoever asked for it.
func (s *Service) UnlockSignIn(ctx context.Context, actorID, userID uuid.UUID) error {
	user, err := s.users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := s.limiter.NewGeneration(ctx, accountFamily(user.Login), loginAttemptWindow); err != nil {
		return err
	}
	if err := s.audit.Record(ctx, audit.Entry{
		ActorID:  &actorID,
		Action:   audit.ActionUserSignInUnlock,
		Entity:   "user",
		EntityID: user.ID.String(),
	}); err != nil {
		return fmt.Errorf("record sign-in unlock: %w", err)
	}
	return nil
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
//
// The login is bounded before it is stored. An attempt refused by the address
// budget is recorded before checkThrottle's length guard runs — the budget has
// to be spent first — so the caller's string can be as long as the request
// body. No account's login is longer than users.MaxLoginLength, so the prefix
// kept is all an investigation could ever match on.
func (s *Service) recordFailure(ctx context.Context, cmd LoginCommand, reason string) {
	s.record(ctx, audit.Entry{
		Action:    audit.ActionAuthLoginFailed,
		Entity:    "user",
		Payload:   map[string]any{"login": boundLogin(cmd.Login), "reason": reason},
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

// boundLogin cuts a login to users.MaxLoginLength bytes, backing off to the
// start of a character so the stored value stays valid UTF-8.
func boundLogin(login string) string {
	if len(login) <= users.MaxLoginLength {
		return login
	}
	cut := users.MaxLoginLength
	for cut > 0 && !utf8.RuneStart(login[cut]) {
		cut--
	}
	return strings.ToValidUTF8(login[:cut], "")
}

// resetAccountAddress clears the guessing counter this attempt was counted
// against, under the generation it was counted under.
func (s *Service) resetAccountAddress(ctx context.Context, cmd LoginCommand, gen string) error {
	return s.limiter.Reset(ctx, accountAddressSubject(cmd.Login, cmd.IP, gen))
}

// accountFamily names the throttle generation of one account. It is keyed by
// the normalised login, because the counters are: they are spent before the
// account is looked up, when the login is all there is.
func accountFamily(login string) string { return "login:" + normalizeLogin(login) }

// trustedSubject keys the account's trusted budget: every trusted browser of
// the account together. The generation is hex digits only, so the separator
// after it is unambiguous whatever the login holds.
func trustedSubject(login, gen string) string {
	return "trusted:" + gen + "|" + normalizeLogin(login)
}

// deviceSubject keys a trusted browser's guessing limit. The generation is hex
// digits only and the device id base64url, so neither holds the separator.
func deviceSubject(id DeviceID, gen string) string {
	return "device:" + gen + "|" + id.String()
}

// accountSubject keys the account-wide ceiling. The generation is hex digits
// only, so the separator after it is unambiguous whatever the login holds.
func accountSubject(login, gen string) string {
	return "login:" + gen + "|" + normalizeLogin(login)
}

// accountAddressSubject keys the guessing limit. The address is grouped the
// way every address budget is (httpx.AddressSubject: an IPv6 /64 is one
// caller). The address comes first and no address contains the separator, the
// generation is hex digits only, so the split is unambiguous whatever the login
// holds: no login a caller invents can produce another address's key.
func accountAddressSubject(login, ip, gen string) string {
	return "login-from:" + httpx.AddressSubject(ip) + "|" + gen + "|" + normalizeLogin(login)
}

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
