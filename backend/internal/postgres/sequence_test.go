package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func TestSequenceHonoursTheGateContract(t *testing.T) {
	conteststest.SequentialGateContract(t, func(t *testing.T, run func(context.Context, conteststest.SequenceTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-seq")
			student := makeUser(t, ctx, "student-seq")
			contestID := makeContest(t, ctx, author.ID)
			now := txNow(t, ctx)
			others := 0
			run(ctx, conteststest.SequenceTarget{
				Gate:           NewSequence(testPool),
				Questions:      NewQuestions(testPool),
				Submissions:    NewSubmissions(testPool),
				ContestID:      contestID,
				RegistrationID: makeRegistration(t, ctx, contestID, student.ID),
				NewRegistration: func() uuid.UUID {
					others++
					other := makeUser(t, ctx, fmt.Sprintf("other-seq-%d", others))
					return makeRegistration(t, ctx, contestID, other.ID)
				},
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				Now:        func() time.Time { return now },
			})
		})
	})
}
