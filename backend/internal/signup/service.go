package signup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
	// Renew stores a new code and its times, and clears the tries.
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
		cfg.Outbox == nil || cfg.Texts == nil || cfg.Settings == nil || cfg.UnitOfWork == nil {
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
// texts are not written in falls back to the installation's.
type StartCommand struct {
	FullName, Login, Email, Password, Lang string
}

type ConfirmCommand struct {
	Email, Code string
}

// Start validates the form and mails a code to the address; the account is
// made by Confirm. For an address that already has an account it stores
// nothing and mails the owner instead, after the same password hash, so the
// answer and its time do not tell the two apart. A taken login is refused
// openly: logins are public on the leaderboards.
func (s *Service) Start(ctx context.Context, cmd StartCommand) error {
	if !s.available() {
		return ErrNotAvailable
	}
	login, fullName := strings.TrimSpace(cmd.Login), strings.TrimSpace(cmd.FullName)
	email, err := normalizeEmail(cmd.Email)
	if err != nil {
		return err
	}
	if err := users.ValidateAccount(login, fullName, email); err != nil {
		return err
	}
	if err := users.ValidatePassword(cmd.Password); err != nil {
		return err
	}

	// Everything refused before the hash is refused for a known address too.
	now := s.cfg.Now()
	if err := s.refuseTakenLogin(ctx, login, email, now); err != nil {
		return err
	}
	waiting, err := s.cfg.Store.Waiting(ctx, email, now)
	if err != nil {
		return err
	}
	if waiting >= MaxPending {
		return ErrFull
	}
	passwordHash, err := s.cfg.Hasher.Hash(ctx, cmd.Password)
	if err != nil {
		return fmt.Errorf("hash the password: %w", err)
	}

	lang, installation, err := s.letterhead(ctx, cmd.Lang)
	if err != nil {
		return err
	}
	owner, err := s.cfg.Directory.ByEmail(ctx, email)
	if err == nil {
		return s.tellTheOwner(ctx, owner, lang, installation)
	}
	if !errors.Is(err, users.ErrNotFound) {
		return err
	}

	code, codeHash, err := s.drawCode()
	if err != nil {
		return err
	}
	message, err := s.codeMessage(email, fullName, code, lang, installation)
	if err != nil {
		return err
	}
	err = s.cfg.UnitOfWork.Do(ctx, func(ctx context.Context) error {
		if err := s.cfg.Store.Put(ctx, Pending{
			Email: email, Login: login, FullName: fullName, PasswordHash: passwordHash,
			CodeHash: codeHash, SentAt: now, ExpiresAt: now.Add(CodeLifetime),
		}, MaxPending, now); err != nil {
			return err
		}
		return s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{message})
	})
	if err != nil {
		return err
	}
	s.cfg.Outbox.Wake()
	return nil
}

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

// tellTheOwner mails the owner of an address someone signed up with. It
// names the owner's own account and nothing the form carried.
func (s *Service) tellTheOwner(ctx context.Context, owner users.User, lang mailing.Language, installation string) error {
	text, err := s.cfg.Texts.SignupExisting(lang, mailing.SignupExistingData{
		Installation: installation, FullName: owner.FullName, Login: owner.Login,
	})
	if err != nil {
		return fmt.Errorf("write the existing-account message: %w", err)
	}
	if err := s.cfg.Outbox.EnqueueMany(ctx, []mailing.Message{{
		Kind: mailing.KindSignupExisting, UserID: owner.ID, To: owner.Email,
		Subject: text.Subject, Body: text.Body,
	}}); err != nil {
		return err
	}
	s.cfg.Outbox.Wake()
	return nil
}

// Confirm makes the account of the email's sign-up when code is its code,
// and spends the sign-up. Every try counts, a malformed code included. The
// row stays locked from the read to the commit, so two confirmations of one
// code make one account.
func (s *Service) Confirm(ctx context.Context, cmd ConfirmCommand) (users.User, error) {
	if !s.available() {
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

// Resend mails a new code for the email's sign-up, which replaces the old
// one with its tries. An address with no sign-up gets nothing.
func (s *Service) Resend(ctx context.Context, email, lang string) error {
	if !s.available() {
		return ErrNotAvailable
	}
	email, err := normalizeEmail(email)
	if err != nil {
		return nil
	}
	language, installation, err := s.letterhead(ctx, lang)
	if err != nil {
		return err
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
		case now.Before(p.SentAt.Add(ResendCooldown)):
			refusal = ErrTooSoon
			return nil
		}
		message, err := s.codeMessage(p.Email, p.FullName, code, language, installation)
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

// Sweep deletes the sign-ups expired at now; an expired one already holds
// nothing, so this only reclaims the rows.
func (s *Service) Sweep(ctx context.Context, now time.Time) (int64, error) {
	return s.cfg.Store.Sweep(ctx, now)
}

func (s *Service) available() bool { return s.cfg.Enabled && s.cfg.Outbox.Enabled() }

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

func (s *Service) codeMessage(to, fullName, code string, lang mailing.Language, installation string) (mailing.Message, error) {
	text, err := s.cfg.Texts.SignupCode(lang, mailing.SignupCodeData{
		Installation: installation, FullName: fullName, Code: code,
	})
	if err != nil {
		return mailing.Message{}, fmt.Errorf("write the code message: %w", err)
	}
	return mailing.Message{
		Kind: mailing.KindSignupCode, UserID: uuid.Nil, To: to, Subject: text.Subject, Body: text.Body,
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
