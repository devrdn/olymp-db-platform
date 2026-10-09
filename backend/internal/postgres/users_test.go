package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/password/passwordtest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	err := mapUserConstraint(uniqueErr("users_email_key"))

	if !errors.Is(err, users.ErrEmailTaken) {
		t.Errorf("err = %v, want ErrEmailTaken", err)
	}
}

func TestUnknownConstraintPassesThrough(t *testing.T) {
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
	// The roles ship in a migration, not optional seed data.
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
		// A delta: the database may already hold other administrators.
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

		// The default listing leaves deleted accounts out.
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

		// Create inserts the status it is given, not the column default.
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

func TestByLoginOfAStringNoLoginCanBeIsNotFoundAndTheTransactionGoesOn(t *testing.T) {
	// A failed statement would abort a roster import's transaction.
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		for _, login := range []string{"ada\x00lovelace", "\xff\xfe", strings.Repeat("a", 101)} {
			if _, err := repo.ByLogin(ctx, login); !errors.Is(err, users.ErrNotFound) {
				t.Errorf("ByLogin(%q) error = %v, want users.ErrNotFound", login, err)
			}
		}
		made := makeUser(t, ctx, "after-the-odd-ones")
		if got, err := repo.ByLogin(ctx, "after-the-odd-ones"); err != nil || got.ID != made.ID {
			t.Errorf("ByLogin() afterwards = (%v, %v), want the account just made", got.ID, err)
		}
	})
}

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

// auth.Service.Login relies on this to tell a former owner the account is
// inaccessible.
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

func TestByLoginBreaksATieAmongSeveralDeletedRowsByRecency(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)

		older := makeUser(t, ctx, "bylogin-several-deleted-ivanov")
		if err := repo.SetStatus(ctx, []uuid.UUID{older.ID}, users.StatusDeleted,
			users.StatusChange{
				Reason: "created by mistake", By: older.ID, At: time.Now().Add(-time.Hour),
			}); err != nil {
			t.Fatalf("SetStatus() for the older row = %v", err)
		}

		newer, err := repo.Create(ctx, users.User{
			Login: "bylogin-several-deleted-ivanov", FullName: "Second Ivanov", PasswordHash: "y",
			Status: users.StatusActive,
		})
		if err != nil {
			t.Fatalf("Create() after the first delete = %v", err)
		}
		if err := repo.SetStatus(ctx, []uuid.UUID{newer.ID}, users.StatusDeleted,
			users.StatusChange{Reason: "left the university", By: newer.ID, At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() for the newer row = %v", err)
		}

		found, err := repo.ByLogin(ctx, "bylogin-several-deleted-ivanov")
		if err != nil {
			t.Fatalf("ByLogin() = %v, want the more recently deleted account rather than ErrNotFound", err)
		}
		if found.ID != newer.ID {
			t.Errorf("ByLogin() = %v, want %v — the more recently deleted row (%v) must win over the older one (%v)",
				found.ID, newer.ID, newer.ID, older.ID)
		}
	})
}

// Runs through users.Service so TakenAmong and the partial unique index on
// login are both exercised.
func TestRestoreIsRefusedByAReclaimedLogin(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		service := users.NewService(repo, audit.New(NewAuditSink(testPool)), storage.NewUnitOfWork(testPool), passwordtest.NewHasher())

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
		// login.
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

func TestSearchMatchesLoginNameOrEmail(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		target := makeUser(t, ctx, "search-target-ivanov")
		if err := repo.UpdateProfile(ctx, target.ID, "Search Target Ivanov", "search-target@example.edu"); err != nil {
			t.Fatalf("UpdateProfile() = %v", err)
		}
		makeUser(t, ctx, "search-target-other")

		for _, query := range []string{"search-target-ivanov", "Target Ivanov", "search-target@example"} {
			found, err := repo.Search(ctx, query, 10)
			if err != nil {
				t.Fatalf("Search(%q) = %v", query, err)
			}
			if len(found) != 1 || found[0].ID != target.ID {
				t.Fatalf("Search(%q) = %+v, want only %v", query, found, target.ID)
			}
			if found[0].Email != "search-target@example.edu" {
				t.Errorf("Search(%q) email = %q, want the account's own address", query, found[0].Email)
			}
		}
	})
}

func TestSearchReturnsACleanEmptyEmailForAnAccountWithoutOne(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		makeUser(t, ctx, "search-noemail-ivanov")

		found, err := repo.Search(ctx, "search-noemail-ivanov", 10)
		if err != nil {
			t.Fatalf("Search() = %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("Search() = %+v, want exactly one match", found)
		}
		if found[0].Email != "" {
			t.Errorf("Search() email = %q, want an empty string for an account with no email set", found[0].Email)
		}
	})
}

func TestSearchExcludesABlockedAccount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		blocked := makeUser(t, ctx, "search-blocked-ivanov")
		if err := repo.UpdateProfile(ctx, blocked.ID, "Search Blocked Ivanov", ""); err != nil {
			t.Fatalf("UpdateProfile() = %v", err)
		}
		if err := repo.SetStatus(ctx, []uuid.UUID{blocked.ID}, users.StatusBlocked,
			users.StatusChange{Reason: "test", At: time.Now()}); err != nil {
			t.Fatalf("SetStatus() = %v", err)
		}
		live := makeUser(t, ctx, "search-live-ivanov")
		if err := repo.UpdateProfile(ctx, live.ID, "Search Live Ivanov", ""); err != nil {
			t.Fatalf("UpdateProfile() = %v", err)
		}

		found, err := repo.Search(ctx, "ivanov", 10)
		if err != nil {
			t.Fatalf("Search() = %v", err)
		}
		if len(found) != 1 || found[0].ID != live.ID {
			t.Fatalf("Search() = %+v, want only the live account %v", found, live.ID)
		}
	})
}

// CLAUDE.md rule 3.
func TestSearchEscapesAPercentSoItDoesNotMatchEveryRow(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewUsers(testPool)
		makeUser(t, ctx, "escape-test-account")

		found, err := repo.Search(ctx, "%", 200)
		if err != nil {
			t.Fatalf("Search(%%) = %v", err)
		}
		for _, u := range found {
			if u.Login == "escape-test-account" {
				t.Fatalf("Search(%%) matched %q — the '%%' was read as a wildcard instead of a literal character",
					u.Login)
			}
		}
	})
}

// It EXPLAINs usersSearchMatch without the status condition, which a partial
// index on users could satisfy and so avoid a Seq Scan for the wrong reason.
// With enable_seqscan off, the planner still picks a Seq Scan when it is the
// only valid plan, so one non-indexable disjunct shows up as one.
//
// It runs at two lengths. A pattern under three characters has no complete
// trigram, and GIN then scans its whole index: the plan still names the
// indexes, so only the cost ratio against a long word shows that
// contests.MinDirectoryQueryLength is long enough.
func TestSearchPredicateUsesTheTrigramIndexes(t *testing.T) {
	queries := []string{"ivanov", strings.Repeat("z", contests.MinDirectoryQueryLength)}
	costs := make([]float64, len(queries))

	for i, query := range queries {
		t.Run(fmt.Sprintf("query length %d", len(query)), func(t *testing.T) {
			withTx(t, func(ctx context.Context) {
				q := NewUsers(testPool).querier(ctx)

				if _, err := q.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
					t.Fatalf("disable seq scan: %v", err)
				}

				rows, err := q.Query(ctx,
					`EXPLAIN SELECT u.id FROM users u WHERE `+usersSearchMatch, query)
				if err != nil {
					t.Fatalf("EXPLAIN search predicate: %v", err)
				}
				defer rows.Close()

				var plan strings.Builder
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatalf("scan plan line: %v", err)
					}
					plan.WriteString(line)
					plan.WriteString("\n")
				}
				if err := rows.Err(); err != nil {
					t.Fatalf("read plan: %v", err)
				}

				got := plan.String()
				if strings.Contains(got, "Seq Scan") {
					t.Fatalf("search predicate for query length %d forced a sequential scan even with "+
						"sequential scans disabled — a disjunct in usersSearchMatch is not indexable at "+
						"this length:\n%s", len(query), got)
				}
				for _, idx := range []string{"users_login_trgm_idx", "users_full_name_trgm_idx", "users_email_trgm_idx"} {
					if !strings.Contains(got, idx) {
						t.Errorf("plan for query length %d does not use %s — the OR could not be split "+
							"into index scans:\n%s", len(query), idx, got)
					}
				}
				costs[i] = topPlanCost(t, got)
			})
		})
	}

	// Two indexed lookups differ far less than 20x; GIN's full-index fallback
	// measured about 300x on development data.
	const costRatioCeiling = 20
	if ratio := costs[1] / costs[0]; ratio > costRatioCeiling {
		t.Errorf("the plan at the minimum query length (%q) costs %.0fx the plan for a longer word "+
			"(%.0f vs %.0f) — MinDirectoryQueryLength is short enough that pg_trgm extracts no complete "+
			"trigram and GIN falls back to scanning every entry in the index instead of narrowing the "+
			"search", queries[1], ratio, costs[1], costs[0])
	}
}

// explainCostPattern reads the total cost from "(cost=0.00..97.80 rows=1 ...)".
var explainCostPattern = regexp.MustCompile(`cost=[0-9.]+\.\.([0-9.]+)`)

func topPlanCost(t *testing.T, plan string) float64 {
	t.Helper()
	m := explainCostPattern.FindStringSubmatch(plan)
	if m == nil {
		t.Fatalf("could not find a cost in the plan:\n%s", plan)
	}
	cost, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("parse plan cost %q: %v", m[1], err)
	}
	return cost
}

// queryCounter is a pgx.QueryTracer that counts statements sent.
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// It opens its own traced pool, since testPool is shared with other tests.
func TestSearchDoesNotComputeADiscardedCount(t *testing.T) {
	counter := &queryCounter{}
	pool, err := storagetest.OpenCore(context.Background(), func(cfg *pgxpool.Config) {
		cfg.ConnConfig.Tracer = counter
		// One connection, so the guard's query on a new connection is not
		// counted as Search's.
		cfg.MaxConns = 1
	})
	if err != nil {
		t.Fatalf("open traced pool: %v", err)
	}
	if pool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	defer pool.Close()
	// Discard the guard's query.
	counter.n.Store(0)

	if _, err := NewUsers(pool).Search(context.Background(), "no-such-account-zzz", 10); err != nil {
		t.Fatalf("Search() = %v", err)
	}

	if got := counter.n.Load(); got != 1 {
		t.Errorf("Search() sent %d statements, want exactly 1 (no discarded count(*))", got)
	}
}
