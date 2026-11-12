package federation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Store finds accounts and their identities (internal/postgres.Users).
type Store interface {
	// ByExternalIdentity returns users.ErrNotFound when no account holds it.
	ByExternalIdentity(ctx context.Context, provider, issuer, subject string) (users.User, error)
	TouchExternalIdentity(ctx context.Context, provider, issuer, subject string, at time.Time) error
	// ByEmail ignores case and finds live accounts only.
	ByEmail(ctx context.Context, email string) (users.User, error)
}

// Accounts creates and links accounts (users.Service). Both refuse an
// identity another request linked first with users.ErrExternalIdentityTaken,
// leaving nothing behind. LinkExternal re-reads the account under a lock and
// refuses users.ErrStaffNotLinkable, the blocked and deleted sentinels, and
// users.ErrNotFound when the account no longer holds the identity's email.
type Accounts interface {
	CreateExternal(ctx context.Context, cmd users.CreateExternalCommand) (users.User, error)
	LinkExternal(ctx context.Context, userID uuid.UUID, identity users.ExternalIdentity, verifiedAt time.Time) error
}

// PendingLogins reports the logins sign-ups waiting for their code hold
// (internal/postgres.Signups).
type PendingLogins interface {
	// LoginHeld ignores case; an empty email counts every sign-up.
	LoginHeld(ctx context.Context, login, email string, now time.Time) (bool, error)
}

type Config struct {
	Policy        Policy
	Store         Store
	Accounts      Accounts
	PendingLogins PendingLogins
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Random draws login suffixes; nil is crypto/rand.
	Random io.Reader
}

type Service struct {
	cfg Config
}

// NewService panics on a missing part, so a wiring fault stops the boot.
func NewService(cfg Config) *Service {
	if cfg.Store == nil || cfg.Accounts == nil || cfg.PendingLogins == nil {
		panic("federation: NewService needs every part of its Config")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{cfg: cfg}
}

// Resolve returns the account the claims enter: the one their identity is
// linked to; else, when the email is trusted, the live account holding it,
// linked now and its email marked verified; else a new student account,
// carrying the email only when it is trusted. It refuses ErrInvalidClaims
// before any lookup, ErrStaffAccount, users.ErrAccountBlocked,
// users.ErrAccountDeleted, and ErrSignupClosed where an account would have to
// be created.
func (s *Service) Resolve(ctx context.Context, c Claims) (users.User, error) {
	identity, err := c.identity()
	if err != nil {
		return users.User{}, err
	}
	return s.resolve(ctx, c, identity, true)
}

// resolve decides once. When the accounts changed under it (the address it
// found moved before the link, or a live account took it before the
// creation) and again is set, it decides once more from what is there now.
func (s *Service) resolve(ctx context.Context, c Claims, identity users.ExternalIdentity, again bool) (users.User, error) {
	u, err := s.cfg.Store.ByExternalIdentity(ctx, identity.Provider, identity.Issuer, identity.Subject)
	switch {
	case err == nil:
		return s.enter(ctx, u, identity)
	case !errors.Is(err, users.ErrNotFound):
		return users.User{}, err
	}

	var email string
	if c.EmailTrusted {
		email = identity.Email
	}
	if email != "" {
		u, err := s.cfg.Store.ByEmail(ctx, email)
		switch {
		case err == nil:
			u, err = s.link(ctx, u, identity)
			if again && errors.Is(err, users.ErrNotFound) {
				return s.resolve(ctx, c, identity, false)
			}
			return u, err
		case !errors.Is(err, users.ErrNotFound):
			return users.User{}, err
		}
	}
	if !s.cfg.Policy.CreateAccounts {
		return users.User{}, ErrSignupClosed
	}
	u, err = s.create(ctx, c, identity, email)
	if again && errors.Is(err, users.ErrEmailTaken) {
		return s.resolve(ctx, c, identity, false)
	}
	return u, err
}

// refuse says why the account cannot be entered through a provider, if it
// cannot.
func refuse(u users.User) error {
	switch {
	case u.Status == users.StatusDeleted:
		return users.ErrAccountDeleted
	case !u.IsActive():
		return users.ErrAccountBlocked
	case u.IsStaff():
		return ErrStaffAccount
	}
	return nil
}

func (s *Service) enter(ctx context.Context, u users.User, identity users.ExternalIdentity) (users.User, error) {
	if err := refuse(u); err != nil {
		return users.User{}, err
	}
	if err := s.cfg.Store.TouchExternalIdentity(ctx, identity.Provider, identity.Issuer, identity.Subject, s.cfg.Now()); err != nil {
		return users.User{}, err
	}
	return u, nil
}

// known enters the account another request linked the identity to while this
// one was deciding.
func (s *Service) known(ctx context.Context, identity users.ExternalIdentity) (users.User, error) {
	u, err := s.cfg.Store.ByExternalIdentity(ctx, identity.Provider, identity.Issuer, identity.Subject)
	if err != nil {
		return users.User{}, err
	}
	return s.enter(ctx, u, identity)
}

func (s *Service) link(ctx context.Context, u users.User, identity users.ExternalIdentity) (users.User, error) {
	if err := refuse(u); err != nil {
		return users.User{}, err
	}
	at := s.cfg.Now()
	err := s.cfg.Accounts.LinkExternal(ctx, u.ID, identity, at)
	switch {
	case errors.Is(err, users.ErrExternalIdentityTaken):
		return s.known(ctx, identity)
	case errors.Is(err, users.ErrStaffNotLinkable):
		return users.User{}, ErrStaffAccount
	case err != nil:
		return users.User{}, err
	}
	u.EmailVerifiedAt = &at
	return u, nil
}

// Login generation: the base is cut to maxLoginBase so that any suffix keeps
// the login within users.MaxLoginLength.
const (
	maxLoginBase     = 90
	numberedLogins   = 50
	randomLogins     = 3
	randomLoginBytes = 3
)

func (s *Service) create(ctx context.Context, c Claims, identity users.ExternalIdentity, email string) (users.User, error) {
	base := loginBase(c.LoginHint, c.Name)
	fullName := strings.TrimSpace(c.Name)
	if fullName == "" {
		fullName = base
	}
	now := s.cfg.Now()
	for try := 1; try <= numberedLogins+randomLogins; try++ {
		login, err := s.candidate(base, try)
		if err != nil {
			return users.User{}, err
		}
		held, err := s.cfg.PendingLogins.LoginHeld(ctx, login, "", now)
		if err != nil {
			return users.User{}, err
		}
		if held {
			continue
		}
		u, err := s.cfg.Accounts.CreateExternal(ctx, users.CreateExternalCommand{
			Login: login, FullName: fullName, Email: email, VerifiedAt: now, Identity: identity,
		})
		switch {
		case err == nil:
			return u, nil
		case errors.Is(err, users.ErrLoginTaken):
			continue
		case errors.Is(err, users.ErrExternalIdentityTaken):
			return s.known(ctx, identity)
		default:
			return users.User{}, err
		}
	}
	return users.User{}, fmt.Errorf("%w: no free login derived from %q", users.ErrLoginTaken, base)
}

// candidate is the try-th login for base: base itself, then base-2 up to
// base-50, then base with a random hex suffix.
func (s *Service) candidate(base string, try int) (string, error) {
	switch {
	case try == 1:
		return base, nil
	case try <= numberedLogins:
		return base + "-" + strconv.Itoa(try), nil
	}
	suffix := make([]byte, randomLoginBytes)
	if _, err := io.ReadFull(s.cfg.Random, suffix); err != nil {
		return "", fmt.Errorf("draw a login suffix: %w", err)
	}
	return base + "-" + hex.EncodeToString(suffix), nil
}

// loginBase is the hint's local part, or else the name, lowercased and
// reduced to [a-z0-9._-] without leading or trailing marks; "user" when
// neither leaves anything.
func loginBase(hint, name string) string {
	if at := strings.LastIndex(hint, "@"); at >= 0 {
		hint = hint[:at]
	}
	for _, source := range []string{hint, name} {
		if base := plainLogin(source); base != "" {
			return base
		}
	}
	return "user"
}

func plainLogin(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if b.Len() == maxLoginBase {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "._-")
}
