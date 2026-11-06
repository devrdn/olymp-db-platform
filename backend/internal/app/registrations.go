package app

import (
	"context"
	"errors"

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
}

var _ registrations.Accounts = registrationAccounts{}

// Create has no actor: the person registers themselves, and the audit trail
// records a nil actor for it.
func (a registrationAccounts) Create(ctx context.Context, n registrations.NewAccount) (registrations.Account, error) {
	created, err := a.users.Create(ctx, users.CreateCommand{
		Login:    n.Login,
		Email:    n.Email,
		FullName: n.FullName,
		Roles:    []string{registrationRole},
		Delivery: users.Delivery{ByEmail: n.ByEmail},
	})
	if errors.Is(err, users.ErrLoginTaken) || errors.Is(err, users.ErrEmailTaken) {
		return registrations.Account{Existing: true}, nil
	}
	if err != nil {
		return registrations.Account{}, err
	}
	return registrations.Account{
		ID: created.User.ID, Login: created.User.Login, OneTimePassword: created.OneTimePassword,
	}, nil
}
