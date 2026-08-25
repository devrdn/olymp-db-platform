package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

func TestStaffEntryCarriesTheAccountItNames(t *testing.T) {
	// The staff screen would otherwise be a page of identifiers.
	withTx(t, func(ctx context.Context) {
		repo := NewContestManagers(testPool)
		author := makeUser(t, ctx, "author-staff")
		id := makeContest(t, ctx, author.ID)

		if err := repo.Grant(ctx, contests.Manager{
			ContestID: id, UserID: author.ID, Role: rbac.RoleOwner, GrantedBy: author.ID,
		}); err != nil {
			t.Fatalf("Grant() = %v", err)
		}

		got, err := repo.Get(ctx, id, author.ID)
		if err != nil {
			t.Fatalf("Get() = %v", err)
		}
		if got.Login != "author-staff" || got.Role != rbac.RoleOwner {
			t.Errorf("staff entry = %+v, want the owner's login and role", got)
		}
	})
}

func TestGrantingAgainChangesTheRoleInsteadOfFailing(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContestManagers(testPool)
		author := makeUser(t, ctx, "author-regrant")
		helper := makeUser(t, ctx, "helper-regrant")
		id := makeContest(t, ctx, author.ID)

		for _, role := range []rbac.ContestRole{rbac.RoleManager, rbac.RoleManager} {
			if err := repo.Grant(ctx, contests.Manager{
				ContestID: id, UserID: helper.ID, Role: role, GrantedBy: author.ID,
			}); err != nil {
				t.Fatalf("Grant() = %v", err)
			}
		}

		if _, err := repo.Get(ctx, id, helper.ID); err != nil {
			t.Errorf("Get() = %v", err)
		}
	})
}

func TestStaffListNamesTheOwnerFirst(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewContestManagers(testPool)
		author := makeUser(t, ctx, "aaa-owner")
		helper := makeUser(t, ctx, "000-helper")
		id := makeContest(t, ctx, author.ID)

		if err := repo.Grant(ctx, contests.Manager{
			ContestID: id, UserID: helper.ID, Role: rbac.RoleManager, GrantedBy: author.ID,
		}); err != nil {
			t.Fatalf("Grant() = %v", err)
		}
		if err := repo.Grant(ctx, contests.Manager{
			ContestID: id, UserID: author.ID, Role: rbac.RoleOwner, GrantedBy: author.ID,
		}); err != nil {
			t.Fatalf("Grant() = %v", err)
		}

		staff, err := repo.List(ctx, id)
		if err != nil {
			t.Fatalf("List() = %v", err)
		}
		if len(staff) != 2 || staff[0].Role != rbac.RoleOwner {
			t.Errorf("staff = %+v, want the owner first even though their login sorts later", staff)
		}
	})
}

func TestRevokingSomebodyWhoIsNotStaffIsNotFound(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-revoke")
		stranger := makeUser(t, ctx, "stranger-revoke")
		id := makeContest(t, ctx, author.ID)

		err := NewContestManagers(testPool).Revoke(ctx, id, stranger.ID)

		if !errors.Is(err, contests.ErrManagerNotFound) {
			t.Errorf("Revoke() = %v, want ErrManagerNotFound", err)
		}
	})
}
