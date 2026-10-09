package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestQuestionsHonoursTheRepositoryContract(t *testing.T) {
	conteststest.QuestionRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.QuestionTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-questions")
			repo := NewQuestions(testPool)
			run(ctx, conteststest.QuestionTarget{
				Repo:       repo,
				Visible:    repo,
				NewContest: func() uuid.UUID { return makeContest(t, ctx, author.ID) },
				Outside:    context.Background(),
			})
		})
	})
}

// The CHECK constraint bounds rows that bypass Question.Validate; the
// points_awarded computation in Submissions.Insert runs in int4 arithmetic.
// The insert goes through Exec to bypass the domain check.
func TestQuestionPointsIsBoundedAtTheDatabaseToo(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-points-bound")
		contestID := makeContest(t, ctx, author.ID)
		const overTheBound = 10_000_001 // maxPoints (question.go) + 1

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO questions (contest_id, ord, kind, points) VALUES ($1, 1, 'text', $2)`,
			contestID, overTheBound)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != checkViolation {
			t.Fatalf("insert with points = %d: err = %v, want a %s check violation", overTheBound, err, checkViolation)
		}
	})
}

// The database bound must not be tighter than the domain's.
func TestQuestionPointsAtTheBoundIsAcceptedByTheDatabase(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-points-at-bound")
		contestID := makeContest(t, ctx, author.ID)
		const atTheBound = 10_000_000 // maxPoints (question.go)

		_, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
			`INSERT INTO questions (contest_id, ord, kind, points) VALUES ($1, 1, 'text', $2)`,
			contestID, atTheBound)
		if err != nil {
			t.Fatalf("insert with points = %d (the bound itself): %v", atTheBound, err)
		}
	})
}
