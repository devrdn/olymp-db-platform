package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// What a single caller can observe of the SQL policy is the contract every
// contests.PolicyStore answers to, the in-memory one the service tests use
// included (conteststest.PolicyStoreContract). What the table refuses by
// constraint is not part of it, and has no test here yet.
func TestSQLPoliciesHonoursTheStoreContract(t *testing.T) {
	conteststest.PolicyStoreContract(t, func(t *testing.T, run func(context.Context, conteststest.PolicyTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-policy")
			now := txNow(t, ctx)
			editors := 0
			run(ctx, conteststest.PolicyTarget{
				Store:      NewSQLPolicies(testPool),
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				NewUser: func() uuid.UUID {
					editors++
					return makeUser(t, ctx, "editor-policy-"+strconv.Itoa(editors)).ID
				},
				Now: func() time.Time { return now },
			})
		})
	})
}
