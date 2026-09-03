package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
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

func TestRoleCatalogueCarriesWhatTheMigrationSeeded(t *testing.T) {
	// The interface offers these, so an empty or misnamed catalogue is a
	// screen with nothing to pick. They ship as a migration rather than as
	// optional seed data, which is what makes asserting on them fair.
	withTx(t, func(ctx context.Context) {
		catalogue, err := NewUsers(testPool).Roles(ctx)
		if err != nil {
			t.Fatalf("Roles() = %v", err)
		}

		byCode := map[string]string{}
		for _, role := range catalogue {
			byCode[role.Code] = role.Name
		}
		for _, code := range []string{"student", "organizer", "admin"} {
			if byCode[code] == "" {
				t.Errorf("role %q is missing or unnamed; catalogue = %+v", code, catalogue)
			}
		}
	})
}

func TestCountingAdministratorsIgnoresTheOnesWhoCannotSignIn(t *testing.T) {
	// The count decides whether the installation may lose an administrator.
	// A blocked one cannot administer, so counting them would let the last
	// usable account be demoted on the strength of one nobody can use.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		before, err := repo.CountActiveWithRole(ctx, "admin")
		if err != nil {
			t.Fatalf("CountActiveWithRole() = %v", err)
		}

		active := makeUser(t, ctx, "admin-active")
		blocked := makeUser(t, ctx, "admin-blocked")

		for _, id := range []uuid.UUID{active.ID, blocked.ID} {
			if err := repo.ReplaceRoles(ctx, id, []string{"admin"}); err != nil {
				t.Fatalf("ReplaceRoles() = %v", err)
			}
		}
		if err := repo.SetStatus(ctx, []uuid.UUID{blocked.ID}, users.StatusBlocked,
			users.StatusChange{By: active.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		count, err := repo.CountActiveWithRole(ctx, "admin")
		if err != nil {
			t.Fatalf("CountActiveWithRole() after = %v", err)
		}
		// A delta, not an absolute: a real installation already has the
		// administrator bootstrap created, and a test that assumed an empty
		// table would pass on a laptop and fail on anything real.
		if count != before+1 {
			t.Errorf("CountActiveWithRole() = %d, want %d: the blocked one must not count",
				count, before+1)
		}
	})
}

func TestListHidesDeletedUnlessAsked(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		live := makeUser(t, ctx, "list-hides-deleted-live")
		gone := makeUser(t, ctx, "list-hides-deleted-gone")
		if err := repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "left the university", By: live.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		// The default listing is the register an administrator reads: a deleted
		// account is not in it. Scoped to these two logins, since a real
		// installation already has other accounts in it.
		found, total, err := repo.List(ctx, users.Filter{Query: "list-hides-deleted"})
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if total != 1 || len(found) != 1 || found[0].ID != live.ID {
			t.Fatalf("List() = %+v, total %d, want only %v", found, total, live.ID)
		}

		// Asked for by name, it is.
		found, total, err = repo.List(ctx, users.Filter{Query: "list-hides-deleted", Status: users.StatusDeleted})
		if err != nil {
			t.Fatalf("List() with status = %v", err)
		}
		if total != 1 || len(found) != 1 || found[0].ID != gone.ID {
			t.Fatalf("List() with status = %+v, total %d, want only %v", found, total, gone.ID)
		}
		if found[0].StatusReason != "left the university" {
			t.Errorf("StatusReason = %q, want %q", found[0].StatusReason, "left the university")
		}
	})
}

func TestDeletedAccountReleasesItsLogin(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		first := makeUser(t, ctx, "released-login-ivanov")
		if err := repo.SetStatus(ctx, []uuid.UUID{first.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "created by mistake", By: first.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		// The whole point of releasing the login: the same one can be created
		// again. Status must be given explicitly — Create inserts exactly the
		// value it is handed rather than relying on the column's default.
		second, err := repo.Create(ctx, users.User{
			Login: "released-login-ivanov", FullName: "Ivanov", PasswordHash: "x",
			Status: users.StatusActive,
		})
		if err != nil {
			t.Fatalf("Create() after delete = %v", err)
		}
		if second.ID == first.ID {
			t.Error("Create() returned the same id as the deleted account")
		}
	})
}
