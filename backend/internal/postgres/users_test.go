package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/jackc/pgx/v5/pgconn"
)

func uniqueErr(constraint string) error {
	return fmt.Errorf("insert: %w", &pgconn.PgError{Code: uniqueViolation, ConstraintName: constraint})
}

func TestLoginConstraintMapsToLoginTaken(t *testing.T) {
	err := mapUserConstraint(uniqueErr("users_login_lower_key"))

	if !errors.Is(err, users.ErrLoginTaken) {
		t.Errorf("err = %v, want ErrLoginTaken", err)
	}
}

func TestEmailConstraintMapsToEmailTaken(t *testing.T) {
	// Before this mapping a duplicate email reported "login already in use":
	// the admin would retry new logins in vain while the email was the problem.
	err := mapUserConstraint(uniqueErr("users_email_key"))

	if !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("err = %v, want ErrEmailTaken", err)
	}
}

func TestUnknownConstraintPassesThrough(t *testing.T) {
	// A violation this mapper does not recognise must surface as itself, not
	// masquerade as a user-facing conflict.
	original := uniqueErr("some_future_index")

	err := mapUserConstraint(original)

	if errors.Is(err, users.ErrLoginTaken) || errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("err = %v, want the original error untouched", err)
	}
}

func TestNonUniqueErrorPassesThrough(t *testing.T) {
	original := errors.New("connection reset")

	if err := mapUserConstraint(original); !errors.Is(err, original) {
		t.Errorf("err = %v, want the original error", err)
	}
}

func TestEscapeLikeNeutralisesPatternMetacharacters(t *testing.T) {
	// A search string is literal text to the admin typing it: '%' must not
	// match everyone and a trailing '\' must not make Postgres reject the
	// pattern with a 500.
	cases := map[string]string{
		`plain`:      `plain`,
		`50%`:        `50\%`,
		`under_line`: `under\_line`,
		`trailing\`:  `trailing\\`,
		`\%_`:        `\\\%\_`,
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}
