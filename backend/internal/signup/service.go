package signup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/mailing"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/settings"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Store keeps the pending sign-ups (internal/postgres). Every method joins the
// transaction ctx carries; times are the caller's clock.
type Store interface {
	// Waiting counts the sign-ups unexpired at now, the email's own aside.
	Waiting(ctx context.Context, email string, now time.Time) (int, error)
	// LoginHeld reports whether a sign-up of another email, unexpired at now,
	// holds login, ignoring case.
	LoginHeld(ctx context.Context, login, email string, now time.Time) (bool, error)
	// Put stores p in place of the email's sign-up, removing an expired one of
	// another email that holds the login. Under one lock it refuses
	// users.ErrLoginTaken while an unexpired one holds the login, and ErrFull
	// once max others are waiting. No refusal aborts the transaction in ctx.
	Put(ctx context.Context, p Pending, max int, now time.Time) error
	// Take reads the email's sign-up and locks it until the transaction in ctx
	// ends, which ctx must carry; false when there is none.
	Take(ctx context.Context, email string) (Pending, bool, error)
	CountTry(ctx context.Context, id uuid.UUID) error
	// Renew stores a new code and its times; the tries and the token stay.
	Renew(ctx context.Context, id uuid.UUID, codeHash []byte, sentAt, expiresAt time.Time) error
	Delete(ctx context.Context, id uuid.UUID) error
	// Sweep deletes every sign-up expired at now.
	Sweep(ctx context.Context, now time.Time) (int64, error)
}

// Directory finds accounts (users.Repository).
type Directory interface {
	// ByLogin may return a deleted account, whose login is free.
	ByLogin(ctx context.Context, login string) (users.User, error)
	// ByEmail ignores case and finds live accounts only.
	ByEmail(ctx context.Context, email string) (users.User, error)
	users.ContestManagers
}

// Accounts creates the confirmed account (users.Service).
type Accounts interface {
	CreateVerified(ctx context.Context, cmd users.CreateVerifiedCommand) (users.User, error)
}

// Hasher hashes the chosen password (the sign-in hasher, password.Hasher).
type Hasher interface {
	Hash(ctx context.Context, password string) (string, error)
}

// Outbox queues email (mailing.Outbox).
type Outbox interface {
	Enabled() bool
	// EnqueueMany joins the transaction in ctx and wakes nobody.
	EnqueueMany(ctx context.Context, ms []mailing.Message) error
	Wake()
}

// Texts writes the two messages (mailing.Texts).
type Texts interface {
	SignupCode(lang mailing.Language, data mailing.SignupCodeData) (mailing.Text, error)
	SignupExisting(lang mailing.Language, data mailing.SignupExistingData) (mailing.Text, error)
}

// Settings reads the installation's name and mail language (settings.Service).
type Settings interface {
	All(ctx context.Context) (settings.Values, error)
}

// Limiter counts attempts per subject in a fixed window (auth.Limiter).
type Limiter interface {
	Spend(ctx context.Context, subject string, limit int, window time.Duration) (allowed, firstRefused bool, err error)
}

// MailBudget is the installation's budget of sign-up mail (mailing.Budget,
// sized by MailsPerMinute and MailsPerDay).
type MailBudget interface {
	// Spend counts one message and reports whether the budget admits it.
	Spend(ctx context.Context) (bool, error)
}

type Config struct {
	// Enabled is SELF_SIGNUP_ENABLED.
	Enabled    bool
	Store      Store
	Directory  Directory
	Accounts   Accounts
	Hasher     Hasher
	Outbox     Outbox
	Texts      Texts
	Settings   Settings
	Limiter    Limiter
	MailBudget MailBudget
	UnitOfWork storage.UnitOfWork
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Random draws codes and salts; nil is crypto/rand.
	Random io.Reader
}

type Service struct {
	cfg Config
}

// NewService panics on a missing part, so a wiring fault stops the boot.
func NewService(cfg Config) *Service {
	if cfg.Store == nil || cfg.Directory == nil || cfg.Accounts == nil || cfg.Hasher == nil ||
		cfg.Outbox == nil || cfg.Texts == nil || cfg.Settings == nil || cfg.Limiter == nil ||
		cfg.MailBudget == nil || cfg.UnitOfWork == nil {
		panic("signup: NewService needs every part of its Config")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg}
}

// StartCommand is the sign-up form. Lang is the reader's language; one the
// texts are not written in falls back to the installation's. Sender is the
// caller's address as a rate-limit subject (httpx.ClientSubject); callers
// with none share one.
type StartCommand struct {
	FullName, Login, Email, Password, Lang, Sender string
}

// StartResult is what the browser that started a sign-up keeps: Confirm and
// Resend take its token, so a later Start for the same address, which
// replaces the row and its token, cannot be finished with the code mailed
// to someone else.
type StartResult struct {
	Token string
}

type ConfirmCommand struct {
	Email, Code, Token string
}

// ResendCommand is a request for a new code; Lang and Sender are as in
// StartCommand.
type ResendCommand struct {
	Email, Token, Lang, Sender string
}

// Start validates the form, stores a pending sign-up with a code and mails
// the code to the address; the account is made by Confirm. An address that
// already has an account gets the same work and the same row, but its owner
// is mailed the existing-account message and the code reaches nobody, so no
// answer of Start, Confirm or Resend tells the two apart. A taken login is
// refused openly: logins are public on the leaderboards.
func (s *Service) Start(ctx context.Context, cmd StartCommand) (StartResult, error) {
	token, err := s.start(ctx, cmd)
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{Token: token}, nil
}

func (s *Service) start(ctx context.Context, cmd StartCommand) (string, error) {
	if !s.Offered() {
		return "", ErrNotAvailable
	}
	login, fullName := strings.ToLower(strings.TrimSpace(cmd.Login)), strings.TrimSpace(cmd.FullName)
	email, err := normalizeEmail(cmd.Email)
	if err != nil {
		return "", err
	}
	if !plainLogin.MatchString(login) {
		return "", fmt.Errorf("%w: the login must be 3 to 64 lowercase letters, digits, hyphens or underscores, "+
			"starting with a letter or a digit", users.ErrInvalidAccount)
	}
	if err := users.ValidateAccount(login, fullName, email); err != nil {
		return "", err
	}
	// The name greets the reader of a message sent to whatever address was
	// typed.
	if !users.IsWrittenAsAName(fullName) {
		return "", fmt.Errorf("%w: the full name must be letters, spaces, hyphens, apostrophes or periods", users.ErrInvalidAccount)
	}
	if err := users.ValidatePassword(cmd.Password); err != nil {
		return "", err
	}

	// Everything is refused before the hash, alike for a known address.
	checkedAt := s.cfg.Now()
	if err := s.refuseTakenLogin(ctx, login, email, checkedAt); err != nil {
		return "", err
	}
	waiting, err := s.cfg.Store.Waiting(ctx, email, checkedAt)
	if err != nil {
		return "", err
	}
	if waiting >= MaxPending {
		return "", ErrFull
	}
	// Spent before the hash, so a refusal costs no hashing slot that a
	// sign-in could use.
	if err := s.admitMail(ctx, "signup", cmd.Sender, email); err != nil {
		return "", err
	}
	passwordHash, err := s.cfg.Hasher.Hash(ctx, cmd.Password)
	if err != nil {
		return "", fmt.Errorf("hash the password: %w", err)
	}

	// The code's window opens once the hash, which may wait for a slot, is
	// done.
	now := s.cfg.Now()
	code, codeHash, err := s.drawCode()
	if err != nil {
		return "", err
	}
	token, tokenHash, err := s.drawToken()
	if err != nil {
		return "", err
	}
	p := Pending{
		Email: email, Login: login, FullName: fullName, PasswordHash: passwordHash,
		CodeHash: codeHash, TokenHash: tokenHash, SentAt: now, ExpiresAt: now.Add(CodeLifetime),
	}
	message, err := s.message(ctx, p, code, cmd.Lang)
	if err != nil {
		return "", err
	}
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		if err := s.cfg.Store.Put(ctx, p, MaxPending, now); err != nil {
			return err
		}
		return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{message})
	})
	if err != nil {
		return "", err
	}
	s.cfg.Outbox.Wake()
	return token, nil
}

// plainLogin is what a self-chosen login may be. The login is mailed to
// whatever address was typed, so it must not carry a pitch, and a dot would
// let a mail client turn it into a link to a domain.
var plainLogin = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,63}$`)

// refuseTakenLogin refuses a login an account or another address's unexpired
// sign-up holds. Put checks the sign-ups again under its lock.
func (s *Service) refuseTakenLogin(ctx context.Context, login, email string, now time.Time) error {
	if err := s.refuseAccountLogin(ctx, login); err != nil {
		return err
	}
	held, err := s.cfg.Store.LoginHeld(ctx, login, email, now)
	if err != nil {
		return err
	}
	if held {
		return users.ErrLoginTaken
	}
	return nil
}

// message is the code for a new address and, for one a live account holds,
// the existing-account message to its owner, naming the owner's own account
// and nothing the form carried.
func (s *Service) message(ctx context.Context, p Pending, code, lang string) (mailing.Message, error) {
	language, installation, err := s.letterhead(ctx, lang)
	if err != nil {
		return mailing.Message{}, err
	}
	owner, err := s.cfg.Directory.ByEmail(ctx, p.Email)
	known := err == nil
	if err != nil && !errors.Is(err, users.ErrNotFound) {
		return mailing.Message{}, err
	}
	// Asked for a new address too, of no account, so the queries Start makes
	// are the same for both.
	reachable, err := users.ReachableByEmail(ctx, s.cfg.Directory, owner)
	if err != nil {
		return mailing.Message{}, err
	}
	if !known {
		return s.codeMessage(p, code, language, installation)
	}
	text, err := s.cfg.Texts.SignupExisting(language, mailing.SignupExistingData{
		Installation: installation, FullName: owner.FullName, Login: owner.Login,
		ResetByAdministrator: !reachable,
	})
	if err != nil {
		return mailing.Message{}, fmt.Errorf("write the existing-account message: %w", err)
	}
	return mailing.Message{
		Kind: mailing.KindSignupExisting, UserID: owner.ID, To: owner.Email,
		Subject: text.Subject, Body: text.Body,
	}, nil
}

// Confirm makes the account of the email's sign-up when code is its code and
// token the one its Start answered, and spends the sign-up. Every try from
// the browser that started it counts, a malformed code included; a missing
// or other token counts nothing, since without it no code confirms, and
// counting would let anyone who knows the address void the sign-up. The
// row stays locked from the read to the commit, so two confirmations of one
// code make one account.
func (s *Service) Confirm(ctx context.Context, cmd ConfirmCommand) (users.User, error) {
	if !s.Offered() {
		return users.User{}, ErrNotAvailable
	}
	email, err := normalizeEmail(cmd.Email)
	if err != nil {
		return users.User{}, ErrWrongCode
	}

	// A refusal is decided inside the transaction and returned after it
	// commits, so the try it counts or the row it spends is kept.
	var created users.User
	var refusal error
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		p, ok, err := s.cfg.Store.Take(ctx, email)
		if err != nil {
			return err
		}
		if !ok {
			refusal = ErrWrongCode
			return nil
		}
		now := s.cfg.Now()
		if p.spent(now) {
			refusal = ErrCodeExpired
			return nil
		}
		if !tokenMatches(p.TokenHash, cmd.Token) {
			refusal = ErrWrongCode
			return nil
		}
		if !codeMatches(p.CodeHash, cmd.Code) {
			refusal = ErrWrongCode
			return s.cfg.Store.CountTry(ctx, p.ID)
		}

		// An account took the login or the address since Start: the row can
		// never succeed, so it is spent.
		if err := s.refuseTakenAccount(ctx, p); err != nil {
			if !errors.Is(err, users.ErrLoginTaken) && !errors.Is(err, users.ErrEmailTaken) {
				return err
			}
			refusal = err
			return s.cfg.Store.Delete(ctx, p.ID)
		}
		if created, err = s.cfg.Accounts.CreateVerified(ctx, users.CreateVerifiedCommand{
			Login: p.Login, Email: p.Email, FullName: p.FullName, PasswordHash: p.PasswordHash, VerifiedAt: now,
		}); err != nil {
			return err
		}
		return s.cfg.Store.Delete(ctx, p.ID)
	})
	if err != nil {
		return users.User{}, err
	}
	if refusal != nil {
		return users.User{}, refusal
	}
	return created, nil
}

// refuseAccountLogin refuses a login a live account holds.
func (s *Service) refuseAccountLogin(ctx context.Context, login string) error {
	account, err := s.cfg.Directory.ByLogin(ctx, login)
	switch {
	case err == nil && account.Status != users.StatusDeleted:
		return users.ErrLoginTaken
	case err != nil && !errors.Is(err, users.ErrNotFound):
		return err
	}
	return nil
}

// refuseTakenAccount refuses p when a live account holds its login or its
// address.
func (s *Service) refuseTakenAccount(ctx context.Context, p Pending) error {
	if err := s.refuseAccountLogin(ctx, p.Login); err != nil {
		return err
	}
	_, err := s.cfg.Directory.ByEmail(ctx, p.Email)
	switch {
	case err == nil:
		return users.ErrEmailTaken
	case !errors.Is(err, users.ErrNotFound):
		return err
	}
	return nil
}

// Resend stores a new code for the email's sign-up, replacing the old one
// and opening a new window but keeping the tries, and mails it, or mails the
// existing-account message again to the owner of an address a live account
// holds. It takes the token Start answered; a missing or other one answers
// ErrWrongCode and counts nothing, as in Confirm. An address with no sign-up
// gets nothing.
func (s *Service) Resend(ctx context.Context, cmd ResendCommand) error {
	if !s.Offered() {
		return ErrNotAvailable
	}
	email, err := normalizeEmail(cmd.Email)
	if err != nil {
		return nil
	}
	code, codeHash, err := s.drawCode()
	if err != nil {
		return err
	}

	sent := false
	var refusal error
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		p, ok, err := s.cfg.Store.Take(ctx, email)
		if err != nil || !ok {
			return err
		}
		now := s.cfg.Now()
		switch {
		case p.spent(now):
			refusal = ErrCodeExpired
			return nil
		case !tokenMatches(p.TokenHash, cmd.Token):
			refusal = ErrWrongCode
			return nil
		case now.Before(p.SentAt.Add(ResendCooldown)):
			refusal = ErrTooSoon
			return nil
		}
		if err := s.admitMail(ctx, "signup_resend", cmd.Sender, email); err != nil {
			if !errors.Is(err, ErrMailBudget) && !errors.Is(err, ErrTooManyCodes) {
				return err
			}
			refusal = err
			return nil
		}
		message, err := s.message(ctx, p, code, cmd.Lang)
		if err != nil {
			return err
		}
		if err := s.cfg.Store.Renew(ctx, p.ID, codeHash, now, now.Add(CodeLifetime)); err != nil {
			return err
		}
		sent = true
		return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{message})
	})
	if err != nil {
		return err
	}
	if sent {
		s.cfg.Outbox.Wake()
	}
	return refusal
}

// admitMail spends the budgets of a message about to be queued, once
// nothing that would refuse the request without mail is left: the caller's
// address, shared by Start and Resend, then the installation's, then the
// email's for the endpoint. Each is more bounded than the next (CLAUDE.md
// rule 5), and an earlier refusal charges nothing later, so the installation
// refusing a person costs them none of their own attempts.
func (s *Service) admitMail(ctx context.Context, endpoint, sender, email string) error {
	allowed, _, err := s.cfg.Limiter.Spend(ctx, "signup_mail:ip:"+sender, MailsPerAddress, time.Minute)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrTooManyCodes
	}
	if allowed, err = s.cfg.MailBudget.Spend(ctx); err != nil {
		return fmt.Errorf("spend the sign-up mail budget: %w", err)
	}
	if !allowed {
		return ErrMailBudget
	}
	allowed, _, err = s.cfg.Limiter.Spend(ctx, endpoint+":email:"+email, MailsPerEmail, time.Hour)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrTooManyCodes
	}
	return nil
}

// Sweep deletes the sign-ups expired at now; an expired one already holds
// nothing, so this only reclaims the rows.
func (s *Service) Sweep(ctx context.Context, now time.Time) (int64, error) {
	return s.cfg.Store.Sweep(ctx, now)
}

// Offered reports whether the installation offers sign-up: the switch is on
// and mail is, so a code can reach someone.
func (s *Service) Offered() bool { return s.cfg.Enabled && s.cfg.Outbox.Enabled() }

// normalizeEmail trims and lowercases an address, refusing one longer than an
// account's: lowercasing can lengthen a letter in bytes.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case email == "":
		return "", fmt.Errorf("%w: email must not be empty", users.ErrInvalidAccount)
	case len(email) > users.MaxEmailLength:
		return "", fmt.Errorf("%w: email must be at most %d characters", users.ErrInvalidAccount, users.MaxEmailLength)
	}
	return email, nil
}

// letterhead picks the reader's language when the texts are written in it,
// the installation's otherwise.
func (s *Service) letterhead(ctx context.Context, lang string) (mailing.Language, string, error) {
	values, err := s.cfg.Settings.All(ctx)
	if err != nil {
		return "", "", fmt.Errorf("read the mail settings: %w", err)
	}
	language := mailing.Language(lang)
	if !slices.Contains(mailing.Languages(), language) {
		language = mailing.Language(values[settings.KeyMailLanguage])
	}
	return language, values[settings.KeyName], nil
}

func (s *Service) codeMessage(p Pending, code string, lang mailing.Language, installation string) (mailing.Message, error) {
	text, err := s.cfg.Texts.SignupCode(lang, mailing.SignupCodeData{
		Installation: installation, FullName: p.FullName, Login: p.Login, Code: code,
	})
	if err != nil {
		return mailing.Message{}, fmt.Errorf("write the code message: %w", err)
	}
	return mailing.Message{
		Kind: mailing.KindSignupCode, UserID: uuid.Nil, To: p.Email, Subject: text.Subject, Body: text.Body,
	}, nil
}

const (
	saltBytes = 16
	// codeSpace is how many codes there are; draws at or past the last whole
	// multiple of it below 2^32 are redrawn, so every code is equally likely.
	codeSpace = 1_000_000
	codeLimit = (1 << 32) / codeSpace * codeSpace
)

// drawCode returns a code and what is stored for it: a fresh salt followed
// by the digest codeDigest makes.
func (s *Service) drawCode() (string, []byte, error) {
	var b [4]byte
	for {
		if _, err := io.ReadFull(s.cfg.Random, b[:]); err != nil {
			return "", nil, fmt.Errorf("draw a code: %w", err)
		}
		if n := binary.BigEndian.Uint32(b[:]); n < codeLimit {
			code := fmt.Sprintf("%0*d", CodeDigits, n%codeSpace)
			salt := make([]byte, saltBytes, saltBytes+sha256.Size)
			if _, err := io.ReadFull(s.cfg.Random, salt); err != nil {
				return "", nil, fmt.Errorf("draw a salt: %w", err)
			}
			return code, append(salt, codeDigest(salt, code)...), nil
		}
	}
}

// drawToken returns a token for the browser and its SHA-256, which is all
// that is stored: the token is random enough to need no salt.
func (s *Service) drawToken() (string, []byte, error) {
	raw := make([]byte, TokenBytes)
	if _, err := io.ReadFull(s.cfg.Random, raw); err != nil {
		return "", nil, fmt.Errorf("draw a token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

// tokenMatches compares in constant time. A candidate of another length
// cannot match, and is not hashed.
func tokenMatches(stored []byte, candidate string) bool {
	if len(stored) != sha256.Size || len(candidate) != base64.RawURLEncoding.EncodedLen(TokenBytes) {
		return false
	}
	digest := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(stored, digest[:]) == 1
}

func codeDigest(salt []byte, code string) []byte {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(code))
	return h.Sum(nil)
}

// codeMatches compares in constant time. A candidate that is not six digits
// cannot match, and is not hashed.
func codeMatches(stored []byte, candidate string) bool {
	if len(stored) != saltBytes+sha256.Size || !wellFormed(candidate) {
		return false
	}
	salt, digest := stored[:saltBytes], stored[saltBytes:]
	return subtle.ConstantTimeCompare(digest, codeDigest(salt, candidate)) == 1
}

func wellFormed(code string) bool {
	if len(code) != CodeDigits {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
