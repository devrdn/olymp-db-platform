package userstest

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/users"
)

// TestByLoginResolvesToTheLiveAccountAfterDeleteAndRecreate guards the
// guarantee ByLogin must give: the real repository resolves a login through
// the partial unique index on lower(login), which allows at most one
// non-deleted row per login, so once a login is deleted and recreated the
// live row is the only sensible answer. The fake used to pick whichever row
// Go's map iteration visited first, which is unspecified order — so this
// runs many fresh repositories, each with a fresh map, to catch the case
// where the deleted row happens to come first.
func TestByLoginResolvesToTheLiveAccountAfterDeleteAndRecreate(t *testing.T) {
	for i := 0; i < 50; i++ {
		repo := New()
		gone := repo.Add(users.User{Login: "ivanov", Status: users.StatusDeleted})
		live := repo.Add(users.User{Login: "ivanov", Status: users.StatusActive})

		got, err := repo.ByLogin(context.Background(), "ivanov")
		if err != nil {
			t.Fatalf("ByLogin() = %v", err)
		}
		if got.ID != live.ID {
			t.Fatalf("ByLogin() = %v, want the live account %v (deleted account %v released this login)",
				got.ID, live.ID, gone.ID)
		}
	}
}

// TestByLoginStillFindsADeletedAccountWithNoLiveRecreation matches the real
// repository: its query filters on nothing but lower(login), so a login
// nobody has recreated yet still resolves to its deleted row. Hiding deleted
// accounts by default is List's job (see the Status handling in both
// repositories' List), not ByLogin's.
func TestByLoginStillFindsADeletedAccountWithNoLiveRecreation(t *testing.T) {
	repo := New()
	gone := repo.Add(users.User{Login: "petrov", Status: users.StatusDeleted})

	got, err := repo.ByLogin(context.Background(), "petrov")
	if err != nil {
		t.Fatalf("ByLogin() = %v", err)
	}
	if got.ID != gone.ID {
		t.Fatalf("ByLogin() = %v, want the deleted account %v", got.ID, gone.ID)
	}
}
