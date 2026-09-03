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

func TestByIDsReturnsWhatExists(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		first := makeUser(t, ctx, "byids-ivanov")
		second := makeUser(t, ctx, "byids-petrov")

		// A missing id is not an error: telling the caller which of its ids
		// exist is the whole job, and the bulk path turns the absent ones into
		// skips.
		found, err := repo.ByIDs(ctx, []uuid.UUID{first.ID, uuid.New(), second.ID})
		if err != nil {
			t.Fatalf("ByIDs() = %v", err)
		}
		if len(found) != 2 {
			t.Fatalf("ByIDs() returned %d accounts, want 2", len(found))
		}
		byID := map[uuid.UUID]users.User{found[0].ID: found[0], found[1].ID: found[1]}
		if _, ok := byID[first.ID]; !ok {
			t.Errorf("ByIDs() is missing %v", first.ID)
		}
		if _, ok := byID[second.ID]; !ok {
			t.Errorf("ByIDs() is missing %v", second.ID)
		}
	})
}

func TestByIDsCollapsesARepeatedID(t *testing.T) {
	// ANY($1) is a membership test, not a join: a caller that (accidentally)
	// repeats an id must not see the account twice.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		u := makeUser(t, ctx, "byids-repeated")

		found, err := repo.ByIDs(ctx, []uuid.UUID{u.ID, u.ID})
		if err != nil {
			t.Fatalf("ByIDs() = %v", err)
		}
		if len(found) != 1 {
			t.Errorf("ByIDs() returned %d accounts for a repeated id, want 1", len(found))
		}
	})
}

func TestTakenAmongFindsRestoreConflicts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		gone := makeUser(t, ctx, "taken-among-ivanov")
		if err := repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "mistake", By: gone.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}
		if _, err := repo.Create(ctx, users.User{
			Login: "taken-among-ivanov", FullName: "Ivanov", PasswordHash: "x", Status: users.StatusActive,
		}); err != nil {
			t.Fatalf("Create() = %v", err)
		}

		// Restoring this one would collide with the live account that took the
		// login. Finding that out before the transaction is what keeps the
		// rest of a bulk restore working.
		taken, err := repo.TakenAmong(ctx, []uuid.UUID{gone.ID})
		if err != nil {
			t.Fatalf("TakenAmong() = %v", err)
		}
		if len(taken) != 1 || taken[0].ID != gone.ID {
			t.Fatalf("TakenAmong() = %+v, want [%v]", taken, gone.ID)
		}
		if !taken[0].Login {
			t.Errorf("TakenAmong()[0].Login = false, want true: the login collided")
		}
		if taken[0].Email {
			t.Errorf("TakenAmong()[0].Email = true, want false: only the login collided")
		}
	})
}

func TestTakenAmongReportsAnEmailCollisionSeparately(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		gone := makeUser(t, ctx, "taken-among-email-ivanov")
		if _, err := repo.querier(ctx).Exec(ctx,
			`UPDATE users SET email = $2 WHERE id = $1`, gone.ID, "ivanov@example.com"); err != nil {
			t.Fatalf("set email: %v", err)
		}
		if err := repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "mistake", By: gone.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}
		if _, err := repo.Create(ctx, users.User{
			Login: "taken-among-email-petrov", Email: "ivanov@example.com",
			FullName: "Petrov", PasswordHash: "x", Status: users.StatusActive,
		}); err != nil {
			t.Fatalf("Create() = %v", err)
		}

		taken, err := repo.TakenAmong(ctx, []uuid.UUID{gone.ID})
		if err != nil {
			t.Fatalf("TakenAmong() = %v", err)
		}
		if len(taken) != 1 || taken[0].ID != gone.ID {
			t.Fatalf("TakenAmong() = %+v, want [%v]", taken, gone.ID)
		}
		if taken[0].Login {
			t.Errorf("TakenAmong()[0].Login = true, want false: the logins differ")
		}
		if !taken[0].Email {
			t.Errorf("TakenAmong()[0].Email = false, want true: the email collided")
		}
	})
}

func TestTakenAmongIgnoresADeletedAccountNobodyReclaimed(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		gone := makeUser(t, ctx, "taken-among-free")
		if err := repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "mistake", By: gone.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		// Nobody took the login back, so a restore would not collide.
		taken, err := repo.TakenAmong(ctx, []uuid.UUID{gone.ID})
		if err != nil {
			t.Fatalf("TakenAmong() = %v", err)
		}
		if len(taken) != 0 {
			t.Errorf("TakenAmong() = %v, want none", taken)
		}
	})
}

func TestBumpSessionGenerationManyRetiresEverySession(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		first := makeUser(t, ctx, "bump-many-first")
		second := makeUser(t, ctx, "bump-many-second")

		// A missing id must not fail the rest: the bulk path resolves accounts
		// first and this only ever sees ones it already checked exist, but the
		// method itself makes no such assumption.
		if err := repo.BumpSessionGenerationMany(ctx, []uuid.UUID{first.ID, uuid.New(), second.ID}); err != nil {
			t.Fatalf("BumpSessionGenerationMany() = %v", err)
		}

		for _, id := range []uuid.UUID{first.ID, second.ID} {
			got, err := repo.ByID(ctx, id)
			if err != nil {
				t.Fatalf("ByID(%v) = %v", id, err)
			}
			if got.SessionGeneration != 1 {
				t.Errorf("account %v session generation = %d, want 1", id, got.SessionGeneration)
			}
		}
	})
}

func TestSetPasswordManyStoresADigestPerAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		first := makeUser(t, ctx, "setpw-many-first")
		second := makeUser(t, ctx, "setpw-many-second")

		err := repo.SetPasswordMany(ctx, []users.Credential{
			{UserID: first.ID, Hash: "hash-one"},
			{UserID: second.ID, Hash: "hash-two"},
		})
		if err != nil {
			t.Fatalf("SetPasswordMany() = %v", err)
		}

		gotFirst, err := repo.ByID(ctx, first.ID)
		if err != nil {
			t.Fatalf("ByID(first) = %v", err)
		}
		if gotFirst.PasswordHash != "hash-one" || !gotFirst.MustChangePassword {
			t.Errorf("first account = %q/%v, want hash-one and must-change",
				gotFirst.PasswordHash, gotFirst.MustChangePassword)
		}

		gotSecond, err := repo.ByID(ctx, second.ID)
		if err != nil {
			t.Fatalf("ByID(second) = %v", err)
		}
		if gotSecond.PasswordHash != "hash-two" {
			t.Errorf("second account password = %q, want hash-two", gotSecond.PasswordHash)
		}
	})
}

func TestSetPasswordManyRefusesADuplicatedAccount(t *testing.T) {
	// Two Credentials naming the same account can carry different hashes;
	// unnest's join has no way to prefer one, so PostgreSQL would apply an
	// unspecified one with no error. This must be refused, not silently
	// resolved, or the password handed to the person may not be the one
	// stored.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		u := makeUser(t, ctx, "setpw-many-dup")

		err := repo.SetPasswordMany(ctx, []users.Credential{
			{UserID: u.ID, Hash: "hash-one"},
			{UserID: u.ID, Hash: "hash-two"},
		})
		if err == nil {
			t.Fatal("SetPasswordMany() with a duplicated account succeeded")
		}
		if want := fmt.Sprintf("set passwords: account %s is named more than once", u.ID); err.Error() != want {
			t.Errorf("err = %q, want %q", err.Error(), want)
		}

		got, err := repo.ByID(ctx, u.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if got.PasswordHash != "not-a-real-hash" {
			t.Errorf("password hash = %q, want the refusal to leave it untouched", got.PasswordHash)
		}
	})
}

func TestReplaceRolesManySetsTheSameRolesOnEveryAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		first := makeUser(t, ctx, "roles-many-first")
		second := makeUser(t, ctx, "roles-many-second")

		if err := repo.ReplaceRolesMany(ctx, []uuid.UUID{first.ID, second.ID},
			[]string{"organizer"}); err != nil {
			t.Fatalf("ReplaceRolesMany() = %v", err)
		}

		for _, id := range []uuid.UUID{first.ID, second.ID} {
			got, err := repo.ByID(ctx, id)
			if err != nil {
				t.Fatalf("ByID(%v) = %v", id, err)
			}
			if len(got.Roles) != 1 || got.Roles[0] != "organizer" {
				t.Errorf("account %v roles = %v, want [organizer]", id, got.Roles)
			}
		}
	})
}

func TestReplaceRolesManyReportsAnUnknownCode(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		u := makeUser(t, ctx, "roles-many-typo")

		err := repo.ReplaceRolesMany(ctx, []uuid.UUID{u.ID}, []string{"organizer", "orgainzer"})
		if err == nil {
			t.Fatal("ReplaceRolesMany() with a typo'd code succeeded")
		}
		if got := err.Error(); got != "assign roles: 1 of 2 codes are not known roles" {
			t.Errorf("err = %q, want it to name exactly one unknown code of two", got)
		}
	})
}

func TestReplaceRolesManyTreatsARepeatedCodeAsOne(t *testing.T) {
	// Roles are a set an account holds. A caller-supplied duplicate is not a
	// second, unknown role — the row-count check must not report it as one.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		first := makeUser(t, ctx, "roles-many-dup-first")
		second := makeUser(t, ctx, "roles-many-dup-second")

		err := repo.ReplaceRolesMany(ctx, []uuid.UUID{first.ID, second.ID},
			[]string{"organizer", "organizer"})
		if err != nil {
			t.Fatalf("ReplaceRolesMany() with a repeated known code = %v", err)
		}

		got, err := repo.ByID(ctx, first.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if len(got.Roles) != 1 || got.Roles[0] != "organizer" {
			t.Errorf("roles = %v, want a single organizer role, not one per repeat", got.Roles)
		}
	})
}

func TestReplaceRolesManyTreatsARepeatedIDAsOne(t *testing.T) {
	// Unlike SetPasswordMany, a repeated id here is harmless: both copies
	// want the identical set of roles. Left alone, the CROSS JOIN insert
	// would produce two identical (user_id, role_id) rows for the repeat and
	// trip the user_roles primary key instead of being absorbed like this.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		u := makeUser(t, ctx, "roles-many-dup-id")

		err := repo.ReplaceRolesMany(ctx, []uuid.UUID{u.ID, u.ID}, []string{"organizer"})
		if err != nil {
			t.Fatalf("ReplaceRolesMany() with a repeated id = %v", err)
		}

		got, err := repo.ByID(ctx, u.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if len(got.Roles) != 1 || got.Roles[0] != "organizer" {
			t.Errorf("roles = %v, want a single organizer role, not one per repeated id", got.Roles)
		}
	})
}
