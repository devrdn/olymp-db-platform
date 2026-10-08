package conteststest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// ManagerTarget is what one case of the contract runs against: a repository
// holding no staff yet, and the means to create what a staff entry hangs off.
// A real schema needs a contest and the accounts to exist before an entry can
// name them, so each implementation fills these its own way: the in-memory
// store mints identifiers and remembers the names, PostgreSQL inserts rows.
type ManagerTarget struct {
	Repo contests.ManagerRepository
	// NewUser creates an account and returns its identifier. The login and
	// full name are what a staff entry carries once appointed.
	NewUser func(login, fullName string) uuid.UUID
	// NewContest creates a contest and returns its identifier.
	NewContest func() uuid.UUID
	// Now is what the store's clock reads when a row is written. An
	// appointment's GrantedAt is that clock, so the contract can only state
	// it in its terms.
	Now func() time.Time
}

// ManagerRepositoryContract is what every contests.ManagerRepository must do,
// run as subtests against one implementation. Both the in-memory Managers and
// postgres.ContestManagers run it, so the store the service tests trust and
// the store production uses are held to the same answers: a rule the fake got
// wrong would otherwise pass every service test and fail only in a contest.
//
// each runs one case: it prepares a fresh target, calls run with it and the
// context to call the repository with, and cleans up afterwards. Only the
// behaviour a single caller can observe is here. What a database refuses by
// constraint is left out on purpose: the contract does not ask a repository to
// refuse a second owner, an unknown account or an unknown contest, which the
// service rules out before it writes.
//
// Logins are lower-case letters throughout, because the staff list is ordered
// by them and the order of anything else depends on the database's collation.
func ManagerRepositoryContract(t *testing.T, each func(t *testing.T, run func(context.Context, ManagerTarget))) {
	grant := func(t *testing.T, ctx context.Context, target ManagerTarget, contest, user uuid.UUID, role rbac.ContestRole, by uuid.UUID) {
		t.Helper()
		if err := target.Repo.Grant(ctx, contests.Manager{ContestID: contest, UserID: user, Role: role, GrantedBy: by}); err != nil {
			t.Fatalf("Grant() = %v", err)
		}
	}
	get := func(t *testing.T, ctx context.Context, target ManagerTarget, contest, user uuid.UUID) contests.Manager {
		t.Helper()
		m, err := target.Repo.Get(ctx, contest, user)
		if err != nil {
			t.Fatalf("Get() = %v", err)
		}
		return m
	}
	notFound := func(t *testing.T, err error, what string) {
		t.Helper()
		if !errors.Is(err, contests.ErrManagerNotFound) {
			t.Errorf("%s error = %v, want ErrManagerNotFound", what, err)
		}
	}
	// logins lists the staff of a contest by login, in the order returned.
	logins := func(t *testing.T, ctx context.Context, target ManagerTarget, contest uuid.UUID) []string {
		t.Helper()
		staff, err := target.Repo.List(ctx, contest)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		names := make([]string, 0, len(staff))
		for _, m := range staff {
			names = append(names, m.Login)
		}
		return names
	}
	t.Run("Get returns the entry Grant wrote, naming the account", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest := target.NewContest()
			owner := target.NewUser("olga", "Olga Ostrovska")
			helper := target.NewUser("hana", "Hana Horvat")
			grant(t, ctx, target, contest, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, contest, helper, rbac.RoleManager, owner)

			got := get(t, ctx, target, contest, helper)

			if got.ContestID != contest || got.UserID != helper {
				t.Errorf("entry for (%s, %s), want (%s, %s)", got.ContestID, got.UserID, contest, helper)
			}
			if got.Login != "hana" || got.FullName != "Hana Horvat" {
				t.Errorf("account = (%q, %q), want (hana, Hana Horvat)", got.Login, got.FullName)
			}
			if got.Role != rbac.RoleManager {
				t.Errorf("Role = %q, want %q", got.Role, rbac.RoleManager)
			}
			if got.GrantedBy != owner {
				t.Errorf("GrantedBy = %s, want the owner, %s", got.GrantedBy, owner)
			}
			if entry := get(t, ctx, target, contest, owner); entry.Role != rbac.RoleOwner || entry.Login != "olga" {
				t.Errorf("the owner's entry = %+v, want the owner olga", entry)
			}
		})
	})

	t.Run("Grant names the account and stamps the time itself", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest := target.NewContest()
			owner := target.NewUser("olga", "Olga Ostrovska")
			helper := target.NewUser("hana", "Hana Horvat")

			// Whatever name and time the caller carries, the entry is the
			// account's own and the store's clock.
			err := target.Repo.Grant(ctx, contests.Manager{
				ContestID: contest, UserID: helper, Role: rbac.RoleManager, GrantedBy: owner,
				Login: "carried-login", FullName: "Carried Name",
				GrantedAt: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("Grant() = %v", err)
			}

			got := get(t, ctx, target, contest, helper)
			if got.Login != "hana" || got.FullName != "Hana Horvat" {
				t.Errorf("account = (%q, %q), want the account's own (hana, Hana Horvat)", got.Login, got.FullName)
			}
			if want := target.Now(); !got.GrantedAt.Equal(want) {
				t.Errorf("GrantedAt = %v, want the store's own clock, %v", got.GrantedAt, want)
			}
		})
	})

	t.Run("Grant again replaces the role and who granted it", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest := target.NewContest()
			first := target.NewUser("olga", "olga")
			second := target.NewUser("pavel", "pavel")
			helper := target.NewUser("hana", "hana")
			grant(t, ctx, target, contest, helper, rbac.RoleManager, first)

			// Appointed again, not refused for already being staff: first in
			// the same role, the service's ordinary path for an existing
			// manager, then in another.
			grant(t, ctx, target, contest, helper, rbac.RoleManager, first)
			grant(t, ctx, target, contest, helper, rbac.RoleOwner, second)

			got := get(t, ctx, target, contest, helper)
			if got.Role != rbac.RoleOwner || got.GrantedBy != second {
				t.Errorf("after the second Grant: role %q granted by %s, want %q granted by %s",
					got.Role, got.GrantedBy, rbac.RoleOwner, second)
			}
			if names := logins(t, ctx, target, contest); !slices.Equal(names, []string{"hana"}) {
				t.Errorf("staff = %v, want hana once", names)
			}

			grant(t, ctx, target, contest, helper, rbac.RoleManager, first)
			if got := get(t, ctx, target, contest, helper); got.Role != rbac.RoleManager || got.GrantedBy != first {
				t.Errorf("after the third Grant: role %q granted by %s, want %q granted by %s",
					got.Role, got.GrantedBy, rbac.RoleManager, first)
			}
		})
	})

	t.Run("List names the owner first and the rest by login", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest := target.NewContest()
			// The owner's login sorts after every other, so only the role can
			// put them first.
			owner := target.NewUser("zoya", "zoya")
			grant(t, ctx, target, contest, target.NewUser("carol", "carol"), rbac.RoleManager, owner)
			grant(t, ctx, target, contest, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, contest, target.NewUser("alice", "alice"), rbac.RoleManager, owner)
			grant(t, ctx, target, contest, target.NewUser("bob", "bob"), rbac.RoleManager, owner)

			staff, err := target.Repo.List(ctx, contest)
			if err != nil {
				t.Fatalf("List() = %v", err)
			}

			if len(staff) == 0 || staff[0].Role != rbac.RoleOwner {
				t.Fatalf("staff = %+v, want the owner first", staff)
			}
			if names, want := logins(t, ctx, target, contest), []string{"zoya", "alice", "bob", "carol"}; !slices.Equal(names, want) {
				t.Errorf("staff logins = %v, want %v", names, want)
			}
			for _, m := range staff[1:] {
				if m.Role != rbac.RoleManager {
					t.Errorf("%s has role %q, want %q", m.Login, m.Role, rbac.RoleManager)
				}
			}
		})
	})

	t.Run("List is empty for a contest nobody staffs", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			staff, err := target.Repo.List(ctx, target.NewContest())
			if err != nil {
				t.Fatalf("List() = %v", err)
			}
			if len(staff) != 0 {
				t.Errorf("staff = %+v, want none", staff)
			}
		})
	})

	t.Run("each contest keeps its own staff", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			owner := target.NewUser("olga", "olga")
			shared := target.NewUser("hana", "hana")
			onlyHere := target.NewUser("ivan", "ivan")
			grant(t, ctx, target, ours, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, ours, shared, rbac.RoleManager, owner)
			grant(t, ctx, target, ours, onlyHere, rbac.RoleManager, owner)
			grant(t, ctx, target, theirs, shared, rbac.RoleOwner, shared)

			if names, want := logins(t, ctx, target, ours), []string{"olga", "hana", "ivan"}; !slices.Equal(names, want) {
				t.Errorf("our staff = %v, want %v", names, want)
			}
			if names, want := logins(t, ctx, target, theirs), []string{"hana"}; !slices.Equal(names, want) {
				t.Errorf("their staff = %v, want %v", names, want)
			}
			// The same person holds a role in each contest of their own.
			if got := get(t, ctx, target, ours, shared); got.Role != rbac.RoleManager {
				t.Errorf("role in our contest = %q, want %q", got.Role, rbac.RoleManager)
			}
			if got := get(t, ctx, target, theirs, shared); got.Role != rbac.RoleOwner {
				t.Errorf("role in their contest = %q, want %q", got.Role, rbac.RoleOwner)
			}
		})
	})

	t.Run("Get finds nobody who does not staff that contest", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			ours, theirs := target.NewContest(), target.NewContest()
			owner := target.NewUser("olga", "olga")
			elsewhere := target.NewUser("hana", "hana")
			stranger := target.NewUser("ivan", "ivan")
			grant(t, ctx, target, ours, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, theirs, elsewhere, rbac.RoleManager, owner)

			_, err := target.Repo.Get(ctx, ours, stranger)
			notFound(t, err, "an account never appointed:")
			_, err = target.Repo.Get(ctx, ours, elsewhere)
			notFound(t, err, "staff of another contest:")
			_, err = target.Repo.Get(ctx, uuid.New(), owner)
			notFound(t, err, "a contest that does not exist:")
		})
	})

	t.Run("Revoke takes that entry away and no other", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest, other := target.NewContest(), target.NewContest()
			owner := target.NewUser("olga", "olga")
			leaving := target.NewUser("hana", "hana")
			staying := target.NewUser("ivan", "ivan")
			grant(t, ctx, target, contest, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, contest, leaving, rbac.RoleManager, owner)
			grant(t, ctx, target, contest, staying, rbac.RoleManager, owner)
			grant(t, ctx, target, other, leaving, rbac.RoleManager, owner)

			if err := target.Repo.Revoke(ctx, contest, leaving); err != nil {
				t.Fatalf("Revoke() = %v", err)
			}

			_, err := target.Repo.Get(ctx, contest, leaving)
			notFound(t, err, "Get() after Revoke:")
			if names, want := logins(t, ctx, target, contest), []string{"olga", "ivan"}; !slices.Equal(names, want) {
				t.Errorf("staff after Revoke = %v, want %v", names, want)
			}
			get(t, ctx, target, other, leaving)

			// Gone means free to be appointed again.
			grant(t, ctx, target, contest, leaving, rbac.RoleManager, owner)
			get(t, ctx, target, contest, leaving)
		})
	})

	t.Run("Revoke reports an entry that is not there", func(t *testing.T) {
		each(t, func(ctx context.Context, target ManagerTarget) {
			contest, other := target.NewContest(), target.NewContest()
			owner := target.NewUser("olga", "olga")
			helper := target.NewUser("hana", "hana")
			stranger := target.NewUser("ivan", "ivan")
			grant(t, ctx, target, contest, owner, rbac.RoleOwner, owner)
			grant(t, ctx, target, other, helper, rbac.RoleManager, owner)

			notFound(t, target.Repo.Revoke(ctx, contest, stranger), "Revoke() for an account never appointed:")
			notFound(t, target.Repo.Revoke(ctx, contest, helper), "Revoke() for another contest's staff:")
			get(t, ctx, target, other, helper)
			if names, want := logins(t, ctx, target, contest), []string{"olga"}; !slices.Equal(names, want) {
				t.Errorf("staff after the refusals = %v, want %v", names, want)
			}

			grant(t, ctx, target, contest, helper, rbac.RoleManager, owner)
			if err := target.Repo.Revoke(ctx, contest, helper); err != nil {
				t.Fatalf("Revoke() = %v", err)
			}
			notFound(t, target.Repo.Revoke(ctx, contest, helper), "a second Revoke():")
		})
	})
}
