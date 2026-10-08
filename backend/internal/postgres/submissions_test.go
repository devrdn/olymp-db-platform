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

// farDeadline is a deadline no test in this package means to trip, so an
// unrelated failure never reads as "the deadline check misfired".
var farDeadline = time.Now().UTC().Add(24 * time.Hour)

// What a single caller can observe of Insert is the contract every
// contests.SubmissionRepository answers to, the in-memory one the service
// tests use included (conteststest.SubmissionRepositoryContract). What
// follows it here is what only the real statement can be asked: its int4
// arithmetic, and the race between two transactions.
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
			// Inside one transaction now() is its start time, so the
			// clock Insert reads is exactly the one read here — through the
			// transaction, as Insert reads it; the pool itself is another
			// session with a clock of its own.
			var now time.Time
			if err := storage.QuerierFrom(ctx, testPool).QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
				t.Fatalf("read the database clock: %v", err)
			}
			run(ctx, conteststest.SubmissionTarget{
				Repo: NewSubmissions(testPool), RegistrationID: registrationID, QuestionID: q.ID,
				Now: func() time.Time { return now },
			})
		})
	})
}

// Finding 4 (corrected): the penalty multiplication used to run in
// PostgreSQL's own int4 arithmetic, which a question anywhere near the
// domain's own points ceiling overflows well before any contest could
// plausibly need this many attempts on one question — the maxPoints doc
// comment in internal/contests/question.go used to claim otherwise. Insert
// now casts that multiplication to bigint, so the combination this test
// drives at — the largest penalty the domain allows on the largest question
// it allows — still succeeds instead of surfacing "integer out of range" as
// a 500 for whichever student's attempt happens to tip it over.
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

		// A 100% penalty (the largest questions.penalty_pct permits) on a
		// question worth the domain's own points ceiling — the combination
		// that makes the penalty multiplication as large as it can ever get
		// for a single attempt.
		const points = 10_000_000
		const penaltyPerAttempt = points

		// 215 already-committed wrong attempts is exactly where
		// 215 * 10,000,000 first exceeds int4's own ceiling
		// (2,147,483,647) — the count Insert's own statement multiplies the
		// penalty by for whichever attempt comes next.
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
