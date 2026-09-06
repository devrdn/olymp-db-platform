package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

func TestNowReadsTheCoreDatabasesClock(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		before := time.Now().UTC().Add(-5 * time.Second)
		got, err := NewSubmissions(testPool).Now(ctx)
		if err != nil {
			t.Fatalf("Now() = %v", err)
		}
		after := time.Now().UTC().Add(5 * time.Second)
		if got.Before(before) || got.After(after) {
			t.Fatalf("Now() = %v, want something close to the wall clock (between %v and %v)", got, before, after)
		}
	})
}

func TestInsertTakesTheNextAttemptNumber(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit")
		student := makeUser(t, ctx, "student-submit")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		first, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong",
			SubmittedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("first Insert() = %v", err)
		}
		if first.AttemptNo != 1 {
			t.Fatalf("first AttemptNo = %d, want 1", first.AttemptNo)
		}

		second, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "still wrong",
			SubmittedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("second Insert() = %v", err)
		}
		if second.AttemptNo != 2 {
			t.Fatalf("second AttemptNo = %d, want 2", second.AttemptNo)
		}
	})
}

func TestInsertRefusesOnceAlreadyCorrect(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-2")
		student := makeUser(t, ctx, "student-submit-2")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		if _, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct", IsCorrect: true, PointsAwarded: 10,
			SubmittedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("first Insert() = %v", err)
		}

		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct again",
			SubmittedAt: time.Now().UTC(),
		})
		if !errors.Is(err, contests.ErrQuestionClosed) {
			t.Fatalf("second Insert() error = %v, want ErrQuestionClosed", err)
		}
	})
}

func TestInsertRefusesOnceMaxAttemptsIsSpent(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-3")
		student := makeUser(t, ctx, "student-submit-3")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		max := 2
		repo := NewSubmissions(testPool)
		for i := 0; i < max; i++ {
			if _, err := repo.Insert(ctx, contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong",
				SubmittedAt: time.Now().UTC(), MaxAttempts: &max,
			}); err != nil {
				t.Fatalf("Insert() attempt %d = %v", i+1, err)
			}
		}

		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "one more",
			SubmittedAt: time.Now().UTC(), MaxAttempts: &max,
		})
		if !errors.Is(err, contests.ErrQuestionClosed) {
			t.Fatalf("error = %v, want ErrQuestionClosed", err)
		}
	})
}

// The guarantee finding 3 asks for, proven under real contention rather than
// asserted from the SQL alone: many goroutines racing Insert against the very
// same registration and question, each in its own transaction and its own
// connection — a single enclosing transaction (withTx) would serialise every
// statement through one connection and the race would never have a chance to
// happen, which is why this runs outside one (see
// TestStartingConcurrentlyProducesOneStartTimeNotTwo's own doc for the
// identical reasoning).
//
// This test does not retry a racer that loses (Insert's own contract does
// not promise that a caller who keeps calling it will eventually get in —
// that is contests.Service.Submit's job, proven at the service level in
// internal/contests/submission_test.go). What it proves here is the safety
// property Insert alone is responsible for: however the racers interleave,
// every attempt number that lands is unique, they are exactly {1, ..., N}
// with no gaps, and N never exceeds max_attempts — see submissions.go's own
// doc for why that holds under any interleaving, not only the one this test
// happens to schedule.
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

	// Questions.Create takes a lock that is only meaningful inside a
	// transaction (lockContest, questions.go) and refuses outside one; this
	// commits its own, separate from the race below, which must run with no
	// enclosing transaction of its own (see the doc above).
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
				Value: "wrong", SubmittedAt: time.Now().UTC(), MaxAttempts: &max,
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
			// Expected outcomes for a racer that arrived too late, or that
			// lost the race for a specific attempt number and was not
			// retried by this test on purpose (see the doc above).
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

	// The table itself agrees with what the racers saw: nobody's row was
	// silently lost, and nothing beyond max_attempts ever landed.
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
