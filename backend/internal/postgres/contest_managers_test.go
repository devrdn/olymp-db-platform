package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

func TestContestManagersHonoursTheRepositoryContract(t *testing.T) {
	conteststest.ManagerRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.ManagerTarget)) {
		withTx(t, func(ctx context.Context) {
			accounts := NewUsers(testPool)
			author := makeUser(t, ctx, "author-staff")
			now := txNow(t, ctx)
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
