package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
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

// TestByIDNamesTheActorWhoChangedStatus proves the join that replaced the
// account card's second GET /users/{id}: the actor's login now comes back on
// the same row, resolved by the query itself rather than a follow-up
// request. Run against the real database (make test-db) rather than the
// in-memory fake, because the LEFT JOIN — and in particular that it is a
// LEFT and not an INNER join — is exactly the part a fake cannot exercise.
func TestByIDNamesTheActorWhoChangedStatus(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		admin := makeUser(t, ctx, "actor-login-admin")
		target := makeUser(t, ctx, "actor-login-target")
		if err := repo.SetStatus(ctx, []uuid.UUID{target.ID}, users.StatusBlocked,
			users.StatusChange{Reason: "cheating", By: admin.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		found, err := repo.ByID(ctx, target.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if found.StatusChangedByLogin != admin.Login {
			t.Errorf("StatusChangedByLogin = %q, want the blocking administrator's login %q",
				found.StatusChangedByLogin, admin.Login)
		}
	})
}

// TestByIDReportsNoActorForAFreshAccount is the LEFT JOIN's other half: an
// account nobody has ever blocked or deleted has NULL in status_changed_by,
// and an INNER join would have dropped the row's status columns — or the
// whole row, depending on how the join was written — instead of reporting
// an empty login.
func TestByIDReportsNoActorForAFreshAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		fresh := makeUser(t, ctx, "actor-login-fresh")

		found, err := repo.ByID(ctx, fresh.ID)
		if err != nil {
			t.Fatalf("ByID() = %v", err)
		}
		if found.StatusChangedByLogin != "" {
			t.Errorf("StatusChangedByLogin = %q, want empty: nobody has changed this account's status",
				found.StatusChangedByLogin)
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

// TestByLoginPrefersTheLiveAccountOverADeletedOne is the guarantee only the
// real database can prove: the partial unique index on lower(login) (WHERE
// status <> 'deleted') promises at most one *live* row per login, not one row
// overall, so once a deleted account's login has been given to a new one, two
// rows legitimately share lower(login). Before ByLogin ordered its result,
// a bare SELECT gave no guarantee which of the two a single-row QueryRow
// returned — sign-in could check a password against the deleted original's
// hash, and creating the replacement could be refused as a duplicate of an
// account that no longer holds the login at all. Run against PostgreSQL,
// never the in-memory fake, because the point being proven is what an
// unordered SELECT against real rows actually returns.
func TestByLoginPrefersTheLiveAccountOverADeletedOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		original := makeUser(t, ctx, "bylogin-prefers-live-ivanov")
		if err := repo.SetStatus(ctx, []uuid.UUID{original.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "created by mistake", By: original.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		recreated, err := repo.Create(ctx, users.User{
			Login: "bylogin-prefers-live-ivanov", FullName: "New Ivanov", PasswordHash: "y",
			Status: users.StatusActive,
		})
		if err != nil {
			t.Fatalf("Create() after delete = %v", err)
		}

		found, err := repo.ByLogin(ctx, "bylogin-prefers-live-ivanov")
		if err != nil {
			t.Fatalf("ByLogin() = %v", err)
		}
		if found.ID != recreated.ID {
			t.Errorf("ByLogin() = %v (status %q), want the live account %v — the deleted original %v must not win",
				found.ID, found.Status, recreated.ID, original.ID)
		}
	})
}

// TestByLoginStillFindsAnAccountThatIsOnlyDeleted is ByLogin's other half:
// when nobody has reclaimed the login, it still resolves to the deleted
// account rather than reporting ErrNotFound. auth.Service.Login depends on
// this — it is what lets a deleted account's own former owner be told the
// account is inaccessible, rather than that no such login ever existed (see
// auth.TestDeletedAccountIsRejectedEvenWithTheRightPassword, which exercises
// the same contract through the in-memory fake).
func TestByLoginStillFindsAnAccountThatIsOnlyDeleted(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		gone := makeUser(t, ctx, "bylogin-deleted-only-ivanov")
		if err := repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "left the university", By: gone.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}

		found, err := repo.ByLogin(ctx, "bylogin-deleted-only-ivanov")
		if err != nil {
			t.Fatalf("ByLogin() = %v, want the deleted account rather than ErrNotFound", err)
		}
		if found.ID != gone.ID {
			t.Errorf("ByLogin() = %v, want %v", found.ID, gone.ID)
		}
	})
}

// TestRestoreIsRefusedByAReclaimedLogin proves a guarantee that only the real
// database can prove: the login a deletion releases is held back by a partial
// unique index (WHERE status <> 'deleted'), not by application code, and a
// restore that would collide with a live account now holding it is refused
// before it happens. Run through users.Service — the code a request actually
// takes, TakenAmong included — rather than by asserting on the repository
// method alone, and against PostgreSQL rather than the in-memory fake,
// because an index is exactly the part a fake cannot exercise.
func TestRestoreIsRefusedByAReclaimedLogin(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		service := users.NewService(repo, audit.New(NewAuditSink(testPool)), storage.NewUnitOfWork(testPool))

		admin := makeUser(t, ctx, "reclaimed-login-admin")
		gone := makeUser(t, ctx, "reclaimed-login-ivanov")
		if err := service.Delete(ctx, admin.ID, gone.ID, "left the university"); err != nil {
			t.Fatalf("Delete() = %v", err)
		}

		// A new account reclaims the login the deletion released.
		if _, err := repo.Create(ctx, users.User{
			Login: "reclaimed-login-ivanov", FullName: "New Ivanov", PasswordHash: "x", Status: users.StatusActive,
		}); err != nil {
			t.Fatalf("Create() = %v", err)
		}

		err := service.Restore(ctx, admin.ID, gone.ID)
		if !errors.Is(err, users.ErrLoginTaken) {
			t.Fatalf("Restore() = %v, want ErrLoginTaken", err)
		}

		// The refusal must leave the account exactly where it was.
		stored, getErr := repo.ByID(ctx, gone.ID)
		if getErr != nil {
			t.Fatalf("ByID() = %v", getErr)
		}
		if stored.Status != users.StatusDeleted {
			t.Errorf("Status = %q, want the refused restore to leave it deleted", stored.Status)
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
