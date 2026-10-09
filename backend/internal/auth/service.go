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

// Brute-force limits, all over the same fixed window: per address against a
// sweep across logins, per account and address against guessing, and an
// account-wide ceiling against guessing spread over many addresses.
const (
	// maxLoginAttemptsPerAccountAddress is the guessing limit. It is keyed
	// on account and address, because logins are public and an account-only
	// key would let anybody lock the owner out.
	maxLoginAttemptsPerAccountAddress = 10
	loginAttemptWindow                = 15 * time.Minute

	// DefaultMaxLoginAttemptsPerAccount is the account-wide ceiling, the
	// backstop against guessing from many addresses. At ten times the
	// guessing limit it takes ten addresses each spending their whole budget.
	// Accepted cost: so wide a guess can hold the account shut for the
	// window.
	DefaultMaxLoginAttemptsPerAccount = 100

	// DefaultMaxLoginAttemptsPerDevice is the guessing limit for a trusted
	// browser, which skips the address and account limits, so a copied
	// device cookie still has a limit.
	DefaultMaxLoginAttemptsPerDevice = 10

	// DefaultMaxTrustedLoginAttemptsPerAccount caps attempts through all of
	// an account's trusted browsers together, so collecting cookies does not
	// multiply the device limit. Only a valid cookie holder can spend it.
	DefaultMaxTrustedLoginAttemptsPerAccount = 20

	// maxPasswordChangeAttempts caps current-password guesses from a
	// borrowed session (CLAUDE.md rule 4).
	maxPasswordChangeAttempts = 10

	// DefaultMaxLoginAttemptsPerAddress is the per-address ceiling. It counts
	// successes too, or an attacker could reset it by signing in to their own
	// account; so it limits people, and must fit a lecture hall behind one NAT
	// address while staying far below what a sweep needs.
	DefaultMaxLoginAttemptsPerAddress = 300

	// waitingPerSlot sizes each address's share of the hashing queue, per
	// slot. At 4 slots and ~100 ms per verification, one address's 32 queued
	// attempts drain in under a second, so others still get served within the
	// wait while a lecture hall mostly queues rather than being refused.
	waitingPerSlot = 8
)

// dummyHash is verified against when the login does not exist, so a missing
// account costs the same time as a wrong password.
var dummyHash = mustHash("a password nobody uses to keep the timing honest")

var (
	// ErrInvalidCredentials covers a wrong password and an unknown login
	// alike, so the form reveals nobody's account.
	ErrInvalidCredentials = errors.New("invalid login or password")
	// ErrAccountBlocked is reported only after the password checked out.
	ErrAccountBlocked  = errors.New("account is blocked")
	ErrTooManyAttempts = errors.New("too many login attempts")
)

// Login-failure reasons in the auth.login_failed audit payload: a closed set
// the interface translates. No finer than the endpoint's own answers: a wrong
// password and an unknown login are both ReasonInvalidCredentials, and every
// throttle is ReasonTooManyAttempts, or the trail would become the oracle the
// endpoint denies.
const (
	// #nosec G101 -- an audit reason code, not a credential.
	ReasonInvalidCredentials = "invalid_credentials"
	ReasonAccountBlocked     = "account_blocked"
	ReasonTooManyAttempts    = "too_many_attempts"
)

type ServiceConfig struct {
	Users    UserStore
	Sessions *SessionStore
	Audit    *audit.Recorder
	Limiter  *Limiter
	Logger   *slog.Logger
	// Passwords must be the process's one hasher, or its limit on concurrent
	// hashes, and so on memory, does not hold.
	Passwords *password.Hasher
	// MaxAttemptsPerAddress zero takes DefaultMaxLoginAttemptsPerAddress.
	MaxAttemptsPerAddress int
	// MaxAttemptsPerAccount zero takes DefaultMaxLoginAttemptsPerAccount.
	MaxAttemptsPerAccount int
	Devices               *DeviceTrust
	// MaxAttemptsPerDevice zero takes DefaultMaxLoginAttemptsPerDevice.
	MaxAttemptsPerDevice int
	// MaxTrustedAttemptsPerAccount zero takes
	// DefaultMaxTrustedLoginAttemptsPerAccount.
	MaxTrustedAttemptsPerAccount int
}

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
	waiting       *waitingQueue
}

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

func (s *Service) DeviceCookieLifetime() time.Duration { return s.devices.TTL() }

func (s *Service) Sessions() *SessionStore { return s.sessions }

type LoginCommand struct {
	Login       string
	Password    string
	IP          string
	UserAgent   string
	DeviceToken string
	// PreviousToken is the session cookie sent by a browser already signed
	// in; a successful sign-in ends that session.
	PreviousToken string
}

type LoginResult struct {
	Token              string
	User               users.User
	MustChangePassword bool
	DeviceToken        string
}

// Login authenticates a set of credentials and issues a session. The order
// of the steps is deliberate:
//
//  1. Spend a budget first: a trusted browser's limits (spendTrusted), or
//     else the address budget and the length guards.
//  2. Look the account up, the same way whether or not it exists.
//  3. If the device cookie no longer vouches, spend the address budget it
//     skipped.
//  4. Take a hashing slot, then (without trust) spend the account's guessing
//     limit and ceiling. The slot comes first, so a refusal for load costs the
//     account nothing, and it is released before any write.
//  5. Always compare, then check the account's state.
//
// Any step returning earlier for one kind of failure than another would be an
// oracle. password.ErrBusy means the attempt spent its step-1 budget but was
// not evaluated.
func (s *Service) Login(ctx context.Context, cmd LoginCommand) (LoginResult, error) {
	// A trusted browser skips the address and account limits, which a rival
	// at the next desk could spend, and spends limits only cookie holders can
	// (spendTrusted). The cookie is checked only within the length bounds,
	// since checking it hashes the login.
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
		// Past the trusted limits the attempt continues untrusted rather than
		// being refused, so a copied cookie cannot lock the owner out.
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

	// A cookie that no longer vouches (password changed, account blocked or
	// deleted, login reused) pays the ordinary limits it skipped.
	if trusted && !(found && device.Vouches(user)) {
		trusted = false
		if err := s.checkAddress(ctx, cmd); err != nil {
			return LoginResult{}, err
		}
	}

	// Read before the slot, so the slot is not held idle across a lookup.
	var gen string
	if !trusted {
		var err error
		if gen, err = s.limiter.Generation(ctx, accountFamily(cmd.Login)); err != nil {
			return LoginResult{}, err
		}
	}

	// No slot: nothing was evaluated or recorded, and known and unknown
	// logins reach this alike.
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
			// Record after releasing the slot.
			slot.Release()
			s.recordFailure(ctx, cmd, ReasonTooManyAttempts)
			return LoginResult{}, ErrTooManyAttempts
		}
	}

	// Verify even without an account, or timing would reveal which logins
	// exist.
	hash := dummyHash
	if found {
		hash = user.PasswordHash
	}
	matched, verifyErr := slot.Verify(hash, cmd.Password)
	// Only writes follow; the rehash takes its own slot.
	slot.Release()
	if verifyErr != nil {
		// A malformed stored digest admits nobody.
		s.log.ErrorContext(ctx, "stored password hash is unusable", "login", cmd.Login, "error", verifyErr)
		matched = false
	}

	if !found || !matched {
		s.recordFailure(ctx, cmd, ReasonInvalidCredentials)
		return LoginResult{}, ErrInvalidCredentials
	}

	// The password checked out, so the owner may be told why.
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

	// End the browser's previous session, or monitoring would see a second
	// device. Done after the new session exists, so a failed sign-in signs
	// nobody out; best-effort.
	if cmd.PreviousToken != "" && cmd.PreviousToken != token {
		if err := s.sessions.Delete(ctx, cmd.PreviousToken); err != nil {
			s.log.WarnContext(ctx, "could not end the replaced session", "error", err)
		}
	}

	now := time.Now().UTC()
	if err := s.users.RecordLogin(ctx, user.ID, now); err != nil {
		// The session is valid; a missing timestamp is not worth failing it.
		s.log.WarnContext(ctx, "could not stamp last login", "user_id", user.ID, "error", err)
	}

	var deviceToken string
	if trusted && s.devices.DueForRenewal(device) {
		// Renewed past half its lifetime under the same device id, and only
		// after the right password, so a copied cookie never extends itself
		// and the device's counts carry over.
		if deviceToken, err = s.devices.Issue(user, device.ID); err != nil {
			s.log.WarnContext(ctx, "could not renew a device cookie", "error", err)
			deviceToken = ""
		}
	}
	if !trusted {
		// Success clears this address's guessing counter, but not the
		// account-wide ceiling, which would hand a distributed guess a fresh
		// budget each time the owner signs in.
		if err := s.resetAccountAddress(ctx, cmd, gen); err != nil {
			s.log.WarnContext(ctx, "could not reset the login throttle", "error", err)
		}

		// A failure to issue the cookie costs only the trust.
		if deviceToken, err = s.devices.Issue(user, DeviceID{}); err != nil {
			s.log.WarnContext(ctx, "could not issue a device cookie", "error", err)
			deviceToken = ""
		}
	}
	// A trusted success resets nothing, or the account's own cookie could
	// sign in without limit, each time taking a slot, a session and an audit
	// row.

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

// Logout ends a session. An unknown token is not an error.
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
// spent even when a later step refuses, so a refusal is not free to retry.
//
// Without a vouching device cookie:
//
//  1. checkAddress spends the address budget first (CLAUDE.md rule 5). Account
//     keys come from whatever login the caller typed; checked first, they
//     would let a refused machine mint unbounded counters and fill the
//     in-process cache until every sign-in is refused.
//  2. checkAddress applies the length guards before either value becomes a
//     key or reaches a hash.
//  3. spendAccount, with the hashing slot held, spends the guessing limit and
//     then the ceiling. Only attempts the guessing limit passed reach the
//     ceiling, so one address cannot shut the owner out everywhere.
//
// spendTrusted replaces all three for a trusted browser. Its keys come from a
// signed cookie and cannot be invented, so it needs no address-first guard.

// withinLengths reports whether the login and password are short enough to
// belong to a real account (users.MaxLoginLength, password.MaxLength).
func withinLengths(cmd LoginCommand) bool {
	return len(cmd.Login) <= users.MaxLoginLength && len(cmd.Password) <= password.MaxLength
}

// spendTrusted spends a trusted browser's limit and then the account's
// trusted budget, under the account's throttle generation so an unlock
// clears them, and reports whether either was spent. It records nothing.
//
// Both count successes too and reset only with their window or an unlock. An
// attempt the device limit refused does not spend the trusted budget, so one
// copied cookie cannot use up what the owner's other browsers share.
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

	// An over-long login would mint a key of its own size; an over-long
	// password cannot match any digest. Both get the ordinary wrong-login
	// answer, after the address budget was spent, with no slot used.
	if !withinLengths(cmd) {
		return ErrInvalidCredentials
	}
	return nil
}

// errAddressQueueFull refuses an attempt whose address already has its share
// of the hashing queue. It wraps password.ErrBusy: nothing was evaluated.
var errAddressQueueFull = fmt.Errorf("%w: this address already has its share of sign-ins waiting", password.ErrBusy)

// holdSlot takes a hashing slot for the attempt. An untrusted attempt joins
// its address's share of the queue and is refused past it. A trusted one
// skips the share, so a rival flooding from the same hall cannot keep the
// owner out; the trusted budgets bound those.
func (s *Service) holdSlot(ctx context.Context, cmd LoginCommand, trusted bool) (*password.Slot, error) {
	if trusted || cmd.IP == "" {
		return s.passwords.Hold(ctx)
	}
	leave, ok := s.waiting.join(httpx.AddressSubject(cmd.IP))
	if !ok {
		return nil, errAddressQueueFull
	}
	// Only waiting is capped.
	defer leave()
	return s.passwords.Hold(ctx)
}

// spendAccount spends the guessing limit and then the ceiling under
// generation gen, and reports whether either refused. The caller records the
// refusal after releasing its slot.
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

// AllowPasswordChange counts one attempt to change the account's own password
// and reports ErrTooManyAttempts once the window is spent (CLAUDE.md rule 4).
// It is keyed by the account: it guards against a second person holding the
// session.
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
// successful change.
func (s *Service) ClearPasswordChangeThrottle(ctx context.Context, userID uuid.UUID) {
	if err := s.limiter.Reset(ctx, passwordChangeSubject(userID)); err != nil {
		s.log.WarnContext(ctx, "could not reset the password-change throttle", "error", err)
	}
}

// UnlockSignIn clears an account's guessing limits and ceiling by moving to a
// new throttle generation, since the keys cannot be listed. Address budgets
// stay: clearing them would reopen a sweep. A failure to record the unlock is
// returned, not logged, since it is an administrator's action.
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

// upgradeHash replaces a digest made with weaker parameters; a failure is
// retried at the next login.
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

// recordFailure writes a failed attempt to the trail: the login, never the
// password, and a Reason* constant. The login is bounded first, since a
// refusal by the address budget precedes the length guard.
func (s *Service) recordFailure(ctx context.Context, cmd LoginCommand, reason string) {
	s.record(ctx, audit.Entry{
		Action:    audit.ActionAuthLoginFailed,
		Entity:    "user",
		Payload:   map[string]any{"login": boundLogin(cmd.Login), "reason": reason},
		IP:        cmd.IP,
		UserAgent: cmd.UserAgent,
	})
}

// record writes an audit entry, logging rather than failing when the trail is
// unavailable, so a journal outage is not an authentication outage.
func (s *Service) record(ctx context.Context, entry audit.Entry) {
	if err := s.audit.Record(ctx, entry); err != nil {
		s.log.ErrorContext(ctx, "could not write audit entry", "action", entry.Action, "error", err)
	}
}

// boundLogin cuts a login to users.MaxLoginLength bytes on a character
// boundary.
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

func (s *Service) resetAccountAddress(ctx context.Context, cmd LoginCommand, gen string) error {
	return s.limiter.Reset(ctx, accountAddressSubject(cmd.Login, cmd.IP, gen))
}

// accountFamily names an account's throttle generation, keyed by normalised
// login because counters are spent before the account is looked up.
func accountFamily(login string) string { return "login:" + normalizeLogin(login) }

// trustedSubject keys the account's trusted budget. The generation is hex,
// so the separator is unambiguous.
func trustedSubject(login, gen string) string {
	return "trusted:" + gen + "|" + normalizeLogin(login)
}

// deviceSubject keys a trusted browser's limit; neither part can hold the
// separator.
func deviceSubject(id DeviceID, gen string) string {
	return "device:" + gen + "|" + id.String()
}

// accountSubject keys the account-wide ceiling. The generation is hex, so the
// separator is unambiguous.
func accountSubject(login, gen string) string {
	return "login:" + gen + "|" + normalizeLogin(login)
}

// accountAddressSubject keys the guessing limit, with the address grouped by
// httpx.AddressSubject (CLAUDE.md rule 9). The address comes first and the
// generation is hex, so no invented login can produce another address's key.
func accountAddressSubject(login, ip, gen string) string {
	return "login-from:" + httpx.AddressSubject(ip) + "|" + gen + "|" + normalizeLogin(login)
}

func passwordChangeSubject(userID uuid.UUID) string { return "pwchange:" + userID.String() }

// mustHash builds dummyHash once at start-up, before any shared hasher
// exists.
func mustHash(plaintext string) string {
	hasher := password.NewHasher(password.HasherConfig{Concurrency: 1})
	hash, err := hasher.Hash(context.Background(), plaintext)
	if err != nil {
		panic("auth: cannot build the timing-equalising hash: " + err.Error())
	}
	return hash
}
