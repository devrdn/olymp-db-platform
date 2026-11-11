package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/devrdn/db-contest/backend/internal/platform/password"
	"github.com/devrdn/db-contest/backend/internal/registrations"
	"github.com/devrdn/db-contest/backend/internal/users"
)

// registrationRole is the role an event registration's account receives.
const registrationRole = "student"

// registrationAccounts makes an event registration's account through
// users.Service, in a transaction of its own, so a taken login or email
// aborts only the account and the registration still lands.
type registrationAccounts struct {
	users *users.Service
	// hasher is the one the account's password is hashed with (the sign-in
	// hasher, which users.Service uses for a SelfService account).
	hasher *password.Hasher
}

var _ registrations.Accounts = registrationAccounts{}

// Create has no actor: the person registers themselves, and the audit trail
// records a nil actor for it.
func (a registrationAccounts) Create(ctx context.Context, n registrations.NewAccount) (registrations.Account, error) {
	created, err := a.users.Create(ctx, users.CreateCommand{
		Login:       n.Login,
		Email:       n.Email,
		FullName:    n.FullName,
		Roles:       []string{registrationRole},
		Delivery:    users.Delivery{ByEmail: n.ByEmail},
		SelfService: true,
	})
	// A taken email is found only when the account is inserted, after its
	// password was hashed; a taken login is refused before any hashing, so
	// with mail on that case spends the hash itself.
	if errors.Is(err, users.ErrEmailTaken) {
		return registrations.Account{Existing: true}, nil
	}
	if errors.Is(err, users.ErrLoginTaken) {
		if n.ByEmail {
			if err := a.Decoy(ctx); err != nil {
				return registrations.Account{}, err
			}
		}
		return registrations.Account{Existing: true}, nil
	}
	if err != nil {
		return registrations.Account{}, err
	}
	return registrations.Account{
		ID: created.User.ID, Login: created.User.Login, OneTimePassword: created.OneTimePassword,
	}, nil
}

// decoyPassword stands for a one-time password nobody will ever hold.
const decoyPassword = "decoy-one-time-password-never-issued"

// Decoy hashes a throwaway value with the hasher Create uses, at the same
// cost.
func (a registrationAccounts) Decoy(ctx context.Context) error {
	if _, err := a.hasher.Hash(ctx, decoyPassword); err != nil {
		return fmt.Errorf("hash the decoy password: %w", err)
	}
	return nil
}
