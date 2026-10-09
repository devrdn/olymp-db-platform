package contests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Errors about submitting an answer (docs/ARCHITECTURE.md §6, §8).
var (
	// ErrAnswerTooLong is a value beyond maxAnswerRunes.
	ErrAnswerTooLong = errors.New("the answer is too long")
	// ErrQuestionClosed is a question this registration may no longer answer:
	// already answered correctly, or every attempt spent. It applies to hidden
	// questions too; "not shown" and "not answerable" are separate decisions
	// (§6.1). A late answer gets ErrDeadlinePassed (standing.go) instead.
	ErrQuestionClosed = errors.New("this question is closed")
	// ErrAttemptConflict is the loser of two concurrent submissions that
	// computed the same attempt number; UNIQUE (registration_id, question_id,
	// attempt_no) let only one land. Submit retries on it, so a caller never
	// sees it bare and no HTTP handler maps it.
	ErrAttemptConflict = errors.New("lost the race for this attempt number")
	// ErrTooManyAttemptConflicts is what a caller sees once every retry has
	// lost the race. It is participant-facing: resubmitting is the remedy.
	ErrTooManyAttemptConflicts = errors.New("too many concurrent submissions to this question; try again")
	// ErrQuestionNotOpen is a question not reachable yet: progression is
	// sequential (§6.1.1) and an earlier question is not closed. The server
	// enforces this; hiding the question in the UI does not stop a direct
	// request.
	ErrQuestionNotOpen = errors.New("this question has not opened yet")
	// ErrNotAChoice is a value for a choice question that is not exactly one
	// of its option identifiers. Grading free text would let a string matching
	// several options (an unanchored pattern) solve the question without
	// choosing. Refused before grading and any write, so it costs no attempt.
	ErrNotAChoice = errors.New("the answer is not one of the question's options")
)

// maxAnswerRunes bounds a submitted answer (CLAUDE.md rule 2). Reference
// answers are a phrase at most, and the value is stored unbounded in
// submissions.value on every attempt.
const maxAnswerRunes = 1000

// maxAttemptRetries bounds Submit's retries after ErrAttemptConflict. The
// unique constraint lets one of k simultaneous submissions win per round, so
// the k-th needs up to k tries; past this bound Submit returns
// ErrTooManyAttemptConflicts rather than keep contending for the same tuple.
const maxAttemptRetries = 5

// attemptBackoffBase is the first retry delay, doubled per retry and jittered
// (attemptBackoff), so a burst of losers does not retry in lockstep.
const attemptBackoffBase = 4 * time.Millisecond

// attemptBackoff is the delay before retry attempt+1: exponential, jittered by
// up to half.
//
// The jitter is scheduling, not secrecy: predicting it only lets someone
// collide on purpose, which they can already do, and the unique constraint
// settles it either way. A cryptographic source would buy nothing and can
// fail, which a retry delay must not.
func attemptBackoff(attempt int) time.Duration {
	base := attemptBackoffBase << uint(attempt) // #nosec G115 -- attempt is bounded by maxAttemptRetries
	// #nosec G404 -- jitter for a retry delay, not a security decision; see above.
	return base/2 + time.Duration(rand.Int64N(int64(base/2)+1))
}

// Submission is one participant's answer, as recorded.
type Submission struct {
	ID             uuid.UUID
	RegistrationID uuid.UUID
	QuestionID     uuid.UUID
	AttemptNo      int
	Value          string
	IsCorrect      bool
	PointsAwarded  int
	SubmittedAt    time.Time
}

// SubmissionRequest is what Insert needs to write one row. MaxAttempts is the
// question's current setting, read so the write can enforce it atomically; it
// is not on Submission because a submission must not remember a limit that can
// change later.
type SubmissionRequest struct {
	RegistrationID uuid.UUID
	QuestionID     uuid.UUID
	Value          string
	IsCorrect      bool
	// Points is the question's face value. Insert computes points_awarded
	// from it (minus PenaltyPerAttempt per committed attempt, floored at zero,
	// §6.1.1) from the same count that assigns the attempt number, so the two
	// cannot disagree.
	Points int
	// PenaltyPerAttempt is the points each committed wrong attempt costs,
	// precomputed by Submit (zero when the scoring mode ignores it).
	PenaltyPerAttempt int
	// Deadline is the participant's deadline plus grace (Gate.closesAt).
	// Insert refuses at or after it, checked against its own clock in the
	// statement that writes submitted_at, so clock, deadline and row cannot
	// disagree (§8).
	Deadline time.Time
	// MaxAttempts is nil for a question with no cap.
	MaxAttempts *int
}

// SubmissionRepository records participants' answers. It has one operation on
// purpose: a count-then-insert or a separate clock read would reopen the race
// and the staleness Insert's single statement closes.
type SubmissionRepository interface {
	// Insert writes one submission, computing its attempt number from the
	// committed history and checking req.Deadline against its own clock, in
	// the same statement. If the question is already answered correctly, every
	// attempt is spent against req.MaxAttempts, or the clock has reached
	// req.Deadline, nothing is inserted and ErrQuestionClosed or
	// ErrDeadlinePassed is returned (deadline first when both apply).
	//
	// Two overlapping calls that compute the same attempt number are settled
	// by UNIQUE (registration_id, question_id, attempt_no): the loser gets
	// ErrAttemptConflict. When req.IsCorrect, points_awarded is req.Points
	// minus req.PenaltyPerAttempt per committed attempt, floored at zero, from
	// the same snapshot as the attempt number (§6.1.1).
	Insert(ctx context.Context, req SubmissionRequest) (Submission, error)
}

// SubmitCommand is a participant answering one question.
//
// Participant and Contest are already resolved by the caller (the answer route
// admits them through Gate.StandingOf). Submit asks the same gate again from
// Address because it is the method that writes and may be reached without the
// route's admission.
//
// Submit adds what the gate cannot answer: whether the question belongs to
// this contest, whether this registration may still answer it, and whether the
// deadline has passed by the database's clock at the moment of the write (§8).
type SubmitCommand struct {
	Participant Participant
	Contest     Contest
	QuestionID  uuid.UUID
	Value       string
	// Address is where the answer came from. The zero value is refused by a
	// restricted contest (Contest.AllowsAddress).
	Address netip.Addr
}

// SubmitOutcome is what Submit hands back; never a reference answer.
// AttemptsRemaining and Closed use the same functions as the read side
// (participant_view.go), so both describe "closed" identically.
type SubmitOutcome struct {
	Correct           bool
	PointsAwarded     int
	AttemptsRemaining *int
	Closed            bool
}

// Submit records one participant's answer to one question, grades it and, if
// it is worth anything, updates their score.
//
// Grading happens once, before the write, and is not repeated on retry: it
// depends only on q and cmd.Value. The points a correct answer earns depend on
// how many wrong attempts already landed (§6.1.1), so Insert computes them
// atomically along with the attempt number.
func (s *Service) Submit(ctx context.Context, cmd SubmitCommand) (SubmitOutcome, error) {
	// Gate first: a refused participant learns nothing about which questions
	// exist, and nothing is started.
	if err := s.gate.StandingOf(cmd.Contest, cmd.Participant, s.now(), cmd.Address).Refusal(); err != nil {
		return SubmitOutcome{}, err
	}
	if utf8.RuneCountInString(cmd.Value) > maxAnswerRunes {
		return SubmitOutcome{}, fmt.Errorf("%w: at most %d characters", ErrAnswerTooLong, maxAnswerRunes)
	}

	q, err := s.questions.ByID(ctx, cmd.QuestionID)
	if err != nil {
		return SubmitOutcome{}, err
	}
	// Another contest's question is "not found", so one contest's questions
	// cannot be probed through another's endpoint.
	if q.ContestID != cmd.Contest.ID {
		return SubmitOutcome{}, ErrQuestionNotFound
	}
	// A hidden question is graded like a visible one: ByID never filters on
	// IsVisible, and "not shown" is not "not answerable" (§6.1).

	// §6.1.1: in a sequential contest, every earlier question must be closed.
	if cmd.Contest.SequentialActive() {
		open, err := s.sequence.Open(ctx, cmd.Contest.ID, cmd.Participant.ID, q.Ord)
		if err != nil {
			return SubmitOutcome{}, fmt.Errorf("check whether question %s has opened: %w", q.ID, err)
		}
		if !open {
			return SubmitOutcome{}, ErrQuestionNotOpen
		}
	}

	// Refused before the clock starts and before any write, so it costs no
	// attempt; the rate budget was already spent (CLAUDE.md rule 13).
	if q.Kind == KindChoice && !q.HasChoice(cmd.Value) {
		return SubmitOutcome{}, ErrNotAChoice
	}

	participant, err := s.startClock(ctx, cmd.Contest, cmd.Participant, cmd.Address)
	if err != nil {
		return SubmitOutcome{}, err
	}

	// The write refuses at the same instant as the gate (Gate.closesAt),
	// computed once outside the retry loop.
	//
	// A Start that reported success but left the clock pending has no
	// deadline; fail closed rather than let them start forever.
	deadline, ok := Deadline(cmd.Contest, participant)
	if !ok {
		return SubmitOutcome{}, ErrContestNotRunning
	}
	writeDeadline := s.gate.closesAt(deadline)

	correct := s.grade(ctx, q, cmd.Value)
	points := awardablePoints(q, cmd.Contest)
	penaltyPerAttempt := penaltyAmount(q, cmd.Contest)

	var result Submission
	for attempt := 0; ; attempt++ {
		result, err = s.submitOnce(ctx, participant.ID, q, cmd.Value, correct, points, penaltyPerAttempt, writeDeadline)
		if !errors.Is(err, ErrAttemptConflict) {
			break
		}
		if attempt >= maxAttemptRetries {
			return SubmitOutcome{}, fmt.Errorf("%w: lost the attempt race %d times in a row: %w",
				ErrTooManyAttemptConflicts, maxAttemptRetries+1, err)
		}
		s.sleep(attemptBackoff(attempt))
	}
	if err != nil {
		return SubmitOutcome{}, err
	}

	return SubmitOutcome{
		Correct:           correct,
		PointsAwarded:     result.PointsAwarded,
		AttemptsRemaining: attemptsRemaining(q.MaxAttempts, result.AttemptNo),
		Closed:            isClosed(q.MaxAttempts, AttemptStats{Attempts: result.AttemptNo, Correct: correct}),
	}, nil
}

// startClock starts an individual participant's clock on their first answer,
// through the registration's Start, which sets started_at only once (§8). It
// then asks the gate again about the participant Start returned: another
// request may have started them first and their time may be up, or they may
// have been disqualified since. Anyone whose clock is not pending is returned
// unchanged.
func (s *Service) startClock(ctx context.Context, c Contest, p Participant, addr netip.Addr) (Participant, error) {
	if !ClockPending(c, p) {
		return p, nil
	}
	started, err := s.registrations.Start(ctx, p.ID, s.now())
	if err != nil {
		return Participant{}, fmt.Errorf("start the participant's clock: %w", err)
	}
	if err := s.gate.StandingOf(c, started, s.now(), addr).Refusal(); err != nil {
		return Participant{}, err
	}
	return started, nil
}

// penaltyAmount is the points one wrong attempt costs (§6.1.1): q.PenaltyPct
// of q.Points, floored, or zero when the result is not points. The organiser's
// percentage is kept, unapplied, in case the scoring mode changes back.
func penaltyAmount(q Question, c Contest) int {
	if c.Scoring == ScoringWinner || c.Scoring == ScoringICPC {
		return 0
	}
	return q.Points * q.PenaltyPct / 100
}

// awardablePoints is the face value Insert computes points_awarded from:
// q.Points, except under ICPC, where questions carry no points and every
// submission writes zero. The configured points are kept, unapplied, in case
// the scoring mode changes back.
func awardablePoints(q Question, c Contest) int {
	if c.Scoring == ScoringICPC {
		return 0
	}
	return q.Points
}

// submitOnce writes one attempt; Insert's single statement does the deadline
// check, attempt number and penalty (§8, §6.1.1). On ErrAttemptConflict
// nothing has taken effect and Submit calls it again.
//
// Only a correct answer with points > 0 needs a transaction, since only it can
// change the score. A zero-point or wrong answer skips the transaction and its
// extra COMMIT round trip.
func (s *Service) submitOnce(ctx context.Context, registrationID uuid.UUID, q Question, value string, correct bool, points, penaltyPerAttempt int, deadline time.Time) (Submission, error) {
	req := SubmissionRequest{
		RegistrationID:    registrationID,
		QuestionID:        q.ID,
		Value:             value,
		IsCorrect:         correct,
		Points:            points,
		PenaltyPerAttempt: penaltyPerAttempt,
		Deadline:          deadline,
		MaxAttempts:       q.MaxAttempts,
	}

	if !correct || points <= 0 {
		return s.submissions.Insert(ctx, req)
	}

	var result Submission
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		result, err = s.submissions.Insert(ctx, req)
		if err != nil {
			return err
		}
		if result.PointsAwarded <= 0 {
			// The penalty consumed the face value: skip a zero-delta write
			// (CLAUDE.md rule 6).
			return nil
		}
		return s.registrations.AddScore(ctx, registrationID, result.PointsAwarded)
	})
	return result, err
}

// matchAnswer reports whether value satisfies one reference answer, by its
// match_kind (§6).
//
// A pattern is anchored to the whole answer (compileAnswerPattern), so it is
// matched against value with only the ends trimmed; otherwise a stray space or
// pasted newline would decide right and wrong. Go's regexp is RE2, linear in
// the input, so maxAnswerRunes is the only bound regex matching needs.
func matchAnswer(a Answer, value string) (bool, error) {
	switch a.MatchKind {
	case MatchExact:
		return value == a.Value, nil
	case MatchExactCI:
		return strings.EqualFold(value, a.Value), nil
	case MatchRegex:
		re, err := compileAnswerPattern(a.Value)
		if err != nil {
			return false, err
		}
		return re.MatchString(strings.TrimSpace(value)), nil
	default:
		// Answer.Validate refuses any other kind; treating an unknown one as
		// a match would score a row nobody validated.
		return false, nil
	}
}

// grade reports whether value matches any reference answer of q; several rows
// per question cover spelling variants.
//
// Patterns are not cached: answers cannot change while a contest runs
// (Contest.ContentEditable), and recompiling a handful of short patterns costs
// microseconds next to a submission's database round trips.
//
// A pattern that fails to compile (Validate already rejects these) counts as no
// match rather than a 500 for everyone reaching the question. It is logged by
// identifier only, never the pattern, so a reference answer never reaches a
// log.
func (s *Service) grade(ctx context.Context, q Question, value string) bool {
	for _, a := range q.Answers {
		ok, err := matchAnswer(a, value)
		if err != nil {
			if s.log != nil {
				s.log.ErrorContext(ctx, "a reference answer's pattern does not compile",
					"question_id", q.ID, "answer_id", a.ID)
			}
			continue
		}
		if ok {
			return true
		}
	}
	return false
}
