package userstest

import (
	"context"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/users"
)

// Runs many fresh repositories, since map order decides which row is seen
// first.
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

// Hiding deleted accounts is List's job, not ByLogin's.
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
