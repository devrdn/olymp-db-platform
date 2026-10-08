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

// What a single caller can observe of the questions is the contract every
// contests.QuestionRepository and contests.VisibleQuestionRepository answers
// to, the in-memory one the service tests use included
// (conteststest.QuestionRepositoryContract). What follows it here is what only
// the real database can be asked: the bounds its columns keep when the domain
// check is not in front of them.
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

// Finding 4: internal/contests.Question.Validate was the only thing bounding
// points before this migration — a row written by hand, or one that predates
// the check, was not, and points_awarded's own computation
// (postgres.Submissions.Insert) multiplies a per-attempt penalty derived
// from it inside Postgres's own int4 arithmetic. This goes straight through
// Exec rather than the repository, the same way TestDurationMinIsBoundedAtTheDatabaseToo
// does for duration_min: the point is the column, not the domain check that
// already exists in front of it.
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

// A points value exactly at the bound is still accepted — this is a
// ceiling, not a tighter limit than the domain's own.
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
