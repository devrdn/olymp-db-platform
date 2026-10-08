package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestAttemptsHonoursTheStoreContract(t *testing.T) {
	conteststest.AttemptStoreContract(t, func(t *testing.T, run func(context.Context, conteststest.AttemptTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-attempts")
			student := makeUser(t, ctx, "student-attempts")
			contestID := makeContest(t, ctx, author.ID)
			now := txNow(t, ctx)
			others := 0
			run(ctx, conteststest.AttemptTarget{
				Store:          NewAttempts(testPool),
				Questions:      NewQuestions(testPool),
				Submissions:    NewSubmissions(testPool),
				ContestID:      contestID,
				RegistrationID: makeRegistration(t, ctx, contestID, student.ID),
				NewRegistration: func() uuid.UUID {
					others++
					other := makeUser(t, ctx, fmt.Sprintf("other-attempts-%d", others))
					return makeRegistration(t, ctx, contestID, other.ID)
				},
				Now: func() time.Time { return now },
			})
		})
	})
}
