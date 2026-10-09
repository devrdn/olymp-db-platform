package postgres

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// The shared contract also run against the in-memory store. Table
// constraints are not covered yet.
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
