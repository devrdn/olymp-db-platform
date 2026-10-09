package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// farDeadline is a deadline no test here means to trip.
var farDeadline = time.Now().UTC().Add(24 * time.Hour)

// The shared contract also run against the in-memory repository; the tests
// below cover what only the real statement can: int4 arithmetic and races.
func TestSubmissionsHonoursTheRepositoryContract(t *testing.T) {
	conteststest.SubmissionRepositoryContract(t, func(t *testing.T, run func(context.Context, conteststest.SubmissionTarget)) {
		withTx(t, func(ctx context.Context) {
			author := makeUser(t, ctx, "author-submit")
			student := makeUser(t, ctx, "student-submit")
			contestID := makeContest(t, ctx, author.ID)
			registrationID := makeRegistration(t, ctx, contestID, student.ID)
			q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
			if err != nil {
				t.Fatalf("Create() = %v", err)
			}
			now := txNow(t, ctx)
			run(ctx, conteststest.SubmissionTarget{
				Repo: NewSubmissions(testPool), RegistrationID: registrationID, QuestionID: q.ID,
				Now: func() time.Time { return now },
			})
		})
	})
}

// The largest points and penalty the domain allows must not overflow the
// penalty product into "integer out of range".
func TestInsertNeverOverflowsInt4AtTheDomainsOwnPointsAndPenaltyCeiling(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-8")
		student := makeUser(t, ctx, "student-submit-8")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		// A 100% penalty on a question worth the points ceiling.
		const points = 10_000_000
		const penaltyPerAttempt = points

		// 215 * 10,000,000 is the first product above int4's 2,147,483,647.
		const alreadyCommitted = 215
		repo := NewSubmissions(testPool)
		for i := 0; i < alreadyCommitted; i++ {
			if _, err := repo.Insert(ctx, contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong",
				Points: points, PenaltyPerAttempt: penaltyPerAttempt, Deadline: farDeadline,
			}); err != nil {
				t.Fatalf("wrong attempt %d: Insert() = %v", i+1, err)
			}
		}

		correct, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct", IsCorrect: true,
			Points: points, PenaltyPerAttempt: penaltyPerAttempt, Deadline: farDeadline,
		})
		if err != nil {
			t.Fatalf("correct attempt after %d wrong ones: Insert() = %v (want no int4 overflow)", alreadyCommitted, err)
		}
		if correct.PointsAwarded != 0 {
			t.Fatalf("PointsAwarded = %d, want 0 (the penalty floors it long before this many attempts)", correct.PointsAwarded)
		}
	})
}

// Racers each use their own connection; withTx would serialise them through
// one. Losers are not retried (that is contests.Service.Submit's job): the
// test checks only that the landed attempt numbers are 1..N, unique, and
// N <= max_attempts.
func TestInsertConcurrentlyNeverExceedsMaxAttemptsOrDuplicatesAnAttemptNumber(t *testing.T) {
	if testPool == nil {
		t.Skip("set CORE_DB_DSN to run the database tests")
	}
	ctx := t.Context()

	author := makeUser(t, ctx, "author-"+uuid.NewString()[:8])
	student := makeUser(t, ctx, "student-"+uuid.NewString()[:8])
	contestID := makeContest(t, ctx, author.ID)
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, contestID)
	})
	registrationID := makeRegistration(t, ctx, contestID, student.ID)

	// Questions.Create refuses to run outside a transaction, so it gets its
	// own committed one.
	var question contests.Question
	err := storage.NewUnitOfWork(testPool).Do(ctx, func(ctx context.Context) error {
		var err error
		question, err = NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		return err
	})
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}

	const (
		racers      = 30
		maxAttempts = 10
	)
	repo := NewSubmissions(testPool)

	type outcome struct {
		submission contests.Submission
		err        error
	}
	results := make(chan outcome, racers)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	for i := range racers {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // all of them go at once
			max := maxAttempts
			s, err := repo.Insert(context.Background(), contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: question.ID,
				Value: "wrong", Deadline: farDeadline, MaxAttempts: &max,
			})
			results <- outcome{s, err}
		}(i)
	}
	start.Done()
	done.Wait()
	close(results)

	seen := map[int]int{}
	succeeded := 0
	for r := range results {
		switch {
		case r.err == nil:
			succeeded++
			seen[r.submission.AttemptNo]++
		case errors.Is(r.err, contests.ErrQuestionClosed), errors.Is(r.err, contests.ErrAttemptConflict):
			// Too late, or lost the race for an attempt number.
		default:
			t.Fatalf("Insert() = %v, want nil, ErrQuestionClosed or ErrAttemptConflict", r.err)
		}
	}

	if succeeded > maxAttempts {
		t.Fatalf("%d submissions succeeded, want at most max_attempts (%d)", succeeded, maxAttempts)
	}
	for attemptNo, count := range seen {
		if count != 1 {
			t.Fatalf("attempt_no %d was recorded %d times, want exactly once", attemptNo, count)
		}
	}
	for n := 1; n <= succeeded; n++ {
		if seen[n] != 1 {
			t.Fatalf("the successful attempt numbers are %v, want exactly {1, ..., %d} with no gaps", seen, succeeded)
		}
	}

	// The table agrees with what the racers saw.
	var stored int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM submissions WHERE registration_id = $1 AND question_id = $2`,
		registrationID, question.ID).Scan(&stored); err != nil {
		t.Fatalf("count stored submissions: %v", err)
	}
	if stored != succeeded {
		t.Fatalf("stored submissions = %d, want %d (what the racers saw succeed)", stored, succeeded)
	}
}
