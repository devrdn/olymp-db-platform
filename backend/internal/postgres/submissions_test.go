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

// farDeadline is a deadline no test below means to trip — every test that is
// not specifically about the deadline uses it, so an unrelated failure never
// reads as "the deadline check misfired".
var farDeadline = time.Now().UTC().Add(24 * time.Hour)

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
			Deadline: farDeadline,
		})
		if err != nil {
			t.Fatalf("first Insert() = %v", err)
		}
		if first.AttemptNo != 1 {
			t.Fatalf("first AttemptNo = %d, want 1", first.AttemptNo)
		}
		if first.SubmittedAt.IsZero() {
			t.Fatal("SubmittedAt is zero, want the database's own now()")
		}

		second, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "still wrong",
			Deadline: farDeadline,
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
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct", IsCorrect: true, Points: 10,
			Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("first Insert() = %v", err)
		}

		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct again",
			Deadline: farDeadline,
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
				Deadline: farDeadline, MaxAttempts: &max,
			}); err != nil {
				t.Fatalf("Insert() attempt %d = %v", i+1, err)
			}
		}

		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "one more",
			Deadline: farDeadline, MaxAttempts: &max,
		})
		if !errors.Is(err, contests.ErrQuestionClosed) {
			t.Fatalf("error = %v, want ErrQuestionClosed", err)
		}
	})
}

// §8, finding 4, finding 5: the deadline is checked against the database's
// own clock inside Insert's own statement, not a value read earlier by the
// caller — proven here by giving Insert a deadline that has already passed
// and confirming the row is refused rather than written.
func TestInsertRefusesAfterTheDeadline(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-4")
		student := makeUser(t, ctx, "student-submit-4")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "too late",
			Deadline: time.Now().UTC().Add(-time.Hour),
		})
		if !errors.Is(err, contests.ErrDeadlinePassed) {
			t.Fatalf("error = %v, want ErrDeadlinePassed", err)
		}

		var stored int
		if err := testPool.QueryRow(ctx,
			`SELECT COUNT(*) FROM submissions WHERE registration_id = $1 AND question_id = $2`,
			registrationID, q.ID).Scan(&stored); err != nil {
			t.Fatalf("count stored submissions: %v", err)
		}
		if stored != 0 {
			t.Fatalf("stored = %d, want 0 — a submission past its deadline must not be written", stored)
		}
	})
}

// When both a passed deadline and a closed question would refuse the write,
// the deadline is what the caller learns about — the same priority this
// codebase gave the two checks before they were folded into Insert's own
// statement (submission.go's own doc).
func TestInsertPrefersDeadlinePassedOverQuestionClosed(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-5")
		student := makeUser(t, ctx, "student-submit-5")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		if _, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct", IsCorrect: true, Points: 10,
			Deadline: farDeadline,
		}); err != nil {
			t.Fatalf("first Insert() = %v", err)
		}

		// The question is already closed (answered correctly above) and the
		// deadline given here has already passed too.
		_, err = repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "too late as well",
			Deadline: time.Now().UTC().Add(-time.Hour),
		})
		if !errors.Is(err, contests.ErrDeadlinePassed) {
			t.Fatalf("error = %v, want ErrDeadlinePassed (priority over an already-closed question)", err)
		}
	})
}

// §6.1.1: points_awarded is computed inside Insert's own statement from the
// same already-committed attempt count the attempt number comes from — two
// wrong attempts at 50% of a 10-point question leave 10 - 2*5 = 0 for a
// correct third try, floored at zero rather than going negative.
func TestInsertAppliesThePenaltyFromTheAlreadyCommittedAttemptCount(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-6")
		student := makeUser(t, ctx, "student-submit-6")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		for i := 0; i < 2; i++ {
			if _, err := repo.Insert(ctx, contests.SubmissionRequest{
				RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong",
				Points: 10, PenaltyPerAttempt: 5, Deadline: farDeadline,
			}); err != nil {
				t.Fatalf("wrong attempt %d: Insert() = %v", i+1, err)
			}
		}

		correct, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "correct", IsCorrect: true,
			Points: 10, PenaltyPerAttempt: 5, Deadline: farDeadline,
		})
		if err != nil {
			t.Fatalf("correct attempt: Insert() = %v", err)
		}
		if correct.PointsAwarded != 0 {
			t.Fatalf("PointsAwarded = %d, want 0 (10 - 2*5, floored at zero)", correct.PointsAwarded)
		}
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

// A wrong attempt always scores zero, whatever Points and PenaltyPerAttempt
// say — the CASE in Insert's own statement takes the ELSE branch outright.
func TestInsertNeverAwardsPointsForAWrongAnswer(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		author := makeUser(t, ctx, "author-submit-7")
		student := makeUser(t, ctx, "student-submit-7")
		contestID := makeContest(t, ctx, author.ID)
		registrationID := makeRegistration(t, ctx, contestID, student.ID)
		q, err := NewQuestions(testPool).Create(ctx, contests.Question{ContestID: contestID, Kind: contests.KindText, IsVisible: true})
		if err != nil {
			t.Fatalf("Create() = %v", err)
		}

		repo := NewSubmissions(testPool)
		wrong, err := repo.Insert(ctx, contests.SubmissionRequest{
			RegistrationID: registrationID, QuestionID: q.ID, Value: "wrong",
			Points: 10, PenaltyPerAttempt: 0, Deadline: farDeadline,
		})
		if err != nil {
			t.Fatalf("Insert() = %v", err)
		}
		if wrong.PointsAwarded != 0 {
			t.Fatalf("PointsAwarded = %d, want 0 for a wrong answer", wrong.PointsAwarded)
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
