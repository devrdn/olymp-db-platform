package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// What a single caller can observe of a contest's staff is the contract every
// contests.ManagerRepository answers to, the in-memory one the service tests
// use included (conteststest.ManagerRepositoryContract). Nothing is left for
// this file to ask of the real database beyond it: the constraints on the
// table (one owner per contest, a real account and a real contest) are
// refused by the database and are not part of what the contract states.
func TestContestManagersHonoursTheRepositoryContract(t *testing.T) {
	conteststest.ManagerRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.ManagerTarget)) {
		withTx(t, func(ctx context.Context) {
			accounts := NewUsers(testPool)
			author := makeUser(t, ctx, "author-staff")
			// Inside one transaction now() is its start time, which is what
			// granted_at is stamped with, so the clock a row is stamped with
			// is exactly the one read here — through the transaction, as the
			// insert reads it; the pool itself is another session.
			var now time.Time
			if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
				t.Fatalf("read the database clock: %v", err)
			}
			run(ctx, conteststest.ManagerTarget{
				Repo: NewContestManagers(testPool),
				NewUser: func(login, fullName string) uuid.UUID {
					created, err := accounts.Create(ctx, users.User{
						Login: login, FullName: fullName, Status: users.StatusActive, PasswordHash: "not-a-real-hash",
					})
					if err != nil {
						t.Fatalf("create user %q: %v", login, err)
					}
					return created.ID
				},
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				Now:        func() time.Time { return now },
			})
		})
	})
}
