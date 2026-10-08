package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

func TestSequenceHonoursTheRepositoryContract(t *testing.T) {
	conteststest.SequentialGateContract(t, func(t *testing.T, run func(context.Context, conteststest.SequenceTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-seq")
			student := makeUser(t, ctx, "student-seq")
			contestID := makeContest(t, ctx, author.ID)
			// Inside one transaction now() is its start time, so the clock
			// Insert checks a deadline against is exactly the one read here
			// — through the transaction, as Insert reads it; the pool itself
			// is another session with a clock of its own.
			var now time.Time
			if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
				t.Fatalf("read the database clock: %v", err)
			}
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
