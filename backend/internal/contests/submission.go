package contests

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	// they already answered it correctly, or every attempt is spent. "Not
	// shown" and "not answerable" are different decisions (§6.1) — this is
	// the second one, and it applies to a hidden question exactly as it does
	// to a visible one, since Submit never consults IsVisible at all.
	ErrQuestionClosed = errors.New("this question is closed")
	// ErrDeadlinePassed is a submission that arrived after this
	// participant's own deadline, checked against the core database's own
	// clock inside the same transaction as the write (§8) — a second,
	// authoritative check, not a repeat of whatever the caller already
	// confirmed on the way in (queryproxy.Service.Access): the two happen at
	// different moments, and only this one gets to be the last word on
	// whether the write lands.
	ErrDeadlinePassed = errors.New("the deadline for this contest has passed")
	// ErrAttemptConflict reports that two submissions to the same question by
	// the same registration computed the same next attempt number at the
	// same moment (finding 3): the table's own
	// UNIQUE (registration_id, question_id, attempt_no) let exactly one of
	// them land, and this is what the loser gets back. It is not a
	// participant-facing outcome — Submit retries on it internally (see
	// maxAttemptRetries) — so it carries no mapping in any HTTP handler and a
	// caller of Submit should never see it returned.
	ErrAttemptConflict = errors.New("lost the race for this attempt number")
)

// maxAnswerRunes bounds a submitted answer.
//
// A reference answer is a name, a short phrase or a choice identifier — never
// more than a sentence (question.go's own Answer never needs more either).
// 1000 runes is generous next to that scale and far below the 1 MiB body
// limit httpx.DecodeJSON already enforces (CLAUDE.md rule 2: the body limit
// bounds the request, not this field) — without it, a participant could send
// a megabyte of text on every one of max_attempts tries, and the whole value
// is stored, unbounded, in submissions.value.
const maxAnswerRunes = 1000

// maxAttemptRetries bounds how many times Submit retries after losing the
// attempt-number race (ErrAttemptConflict): two truly simultaneous
// submissions to the very same question by the very same registration — the
// only thing that ever produces this conflict, since every other submission
// this registration makes targets a different question or arrives after the
// first has already committed. No human doubles-clicks fast enough to need
// more than a couple of retries; the bound exists so a bug that made every
// retry conflict again fails loudly instead of spinning forever.
const maxAttemptRetries = 5

// defaultSubmissionGrace is the network-latency allowance Submit adds to a
// deadline unless ServiceConfig.Grace says otherwise, matching queryproxy's
// own defaultGrace: the two paths add the same margin on top of the one
// contests.Deadline formula (§8), never a grace of their own.
const defaultSubmissionGrace = 5 * time.Second

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

// SubmissionRequest is what Insert needs to write one row. Kept apart from
// Submission (the type Insert hands back) because MaxAttempts is not a
// submission's own field — it is the question's current setting, read at the
// moment of writing so the write itself can enforce it atomically — and
// storing it on Submission would suggest a submission remembers a limit that
// can change after it was made, which it must not (§6.1.1 draws exactly this
// line for a future penalty setting; the same reasoning applies here already).
type SubmissionRequest struct {
	RegistrationID uuid.UUID
	QuestionID     uuid.UUID
	Value          string
	IsCorrect      bool
	PointsAwarded  int
	SubmittedAt    time.Time
	// MaxAttempts is nil for a question with no cap.
	MaxAttempts *int
}

// SubmissionRepository records participants' answers.
//
// Deliberately two operations and no more: a count-then-check-then-insert
// repository is exactly the read-then-write shape the race (finding 3) asks
// this not to be. Everything Submit needs to decide and write an answer
// happens inside Insert's own statement instead.
type SubmissionRepository interface {
	// Now returns the core database's own clock (§8: "по часам core-БД").
	// Submit compares this, not the application server's time.Now, against
	// the participant's deadline — the guarantee that a late answer is
	// refused must not depend on the two processes' clocks agreeing, only on
	// the one clock the write itself lands by.
	Now(ctx context.Context) (time.Time, error)
	// Insert writes one submission row, computing its own attempt number from
	// this registration/question's own history in the same statement rather
	// than reading a count first (finding 3): if the rows already committed
	// for this registration and question show it answered correctly already,
	// or every attempt already spent against req.MaxAttempts, nothing is
	// inserted and ErrQuestionClosed is returned. If two calls truly
	// overlap — both reading a snapshot before either has committed — and
	// compute the same next attempt number, the table's own
	// UNIQUE (registration_id, question_id, attempt_no) lets only one land;
	// the other gets ErrAttemptConflict and Submit retries it from a fresh
	// read (see Service.Submit) rather than this method ever reading the
	// count and then writing it.
	Insert(ctx context.Context, req SubmissionRequest) (Submission, error)
}

// SubmitCommand is a participant answering one question.
//
// Participant and Contest are trusted as already resolved and admitted by
// the caller — queryproxy.Service.Access, the same admission the SQL console
// and the participant-facing read endpoints require (registered, not
// disqualified or finished, the contest running or its own window open, the
// address allowed). Submit does not repeat that check: a second
// implementation of "may this student act here" is the bug this project
// keeps finding (see Reader's own doc for the identical reasoning on the read
// side). contests cannot import queryproxy to call Access itself either way —
// queryproxy is built on top of this package, not the other way round — so
// the caller (internal/api) is where that admission and this command meet.
//
// What Submit adds on top, and Access could never answer on its own: whether
// the question actually belongs to this contest, whether this registration
// may still answer it at all, and whether the deadline has passed by the
// core database's own clock (§8) at the moment of the write rather than the
// moment Access was called.
type SubmitCommand struct {
	Participant Participant
	Contest     Contest
	QuestionID  uuid.UUID
	Value       string
}

// SubmitOutcome is what Submit hands back: never a reference answer, only
// what this participant needs to know about the question after this attempt.
// AttemptsRemaining and Closed are computed the same way ParticipantQuestion
// computes them for the read side (attemptsRemaining, isClosed in
// participant_view.go) — the same two functions, not a second copy of the
// rule — so the read endpoint and this one can never describe "closed"
// differently to the same participant.
type SubmitOutcome struct {
	Correct           bool
	PointsAwarded     int
	AttemptsRemaining *int
	Closed            bool
}

// Submit records one participant's answer to one question, grades it and
// updates their score, all inside one transaction with the deadline check
// (§8).
func (s *Service) Submit(ctx context.Context, cmd SubmitCommand) (SubmitOutcome, error) {
	if utf8.RuneCountInString(cmd.Value) > maxAnswerRunes {
		return SubmitOutcome{}, fmt.Errorf("%w: at most %d characters", ErrAnswerTooLong, maxAnswerRunes)
	}

	q, err := s.questions.ByID(ctx, cmd.QuestionID)
	if err != nil {
		return SubmitOutcome{}, err
	}
	// A question named in the URL that belongs to another contest is
	// "not found" for the same reason questionOf treats it that way
	// (question.go): its existence elsewhere is not this caller's business,
	// and answering differently would let one contest's question set be
	// probed through another contest's own endpoint.
	if q.ContestID != cmd.Contest.ID {
		return SubmitOutcome{}, ErrQuestionNotFound
	}
	// A hidden question is graded exactly like a visible one: q came from
	// QuestionRepository.ByID, the staff read that never filters on
	// IsVisible, and nothing below ever consults that field. "Not shown" and
	// "not answerable" are different decisions (§6.1), and this path only
	// ever makes the second one.

	participant := cmd.Participant
	if cmd.Contest.Timing == TimingIndividual && participant.StartedAt == nil {
		// The same seam queryproxy.Service.Run uses for the identical
		// decision (§8, finding 2): the registration's own Start, which can
		// only ever set started_at once (postgres.Registrations.Start).
		// Nothing here invents a second way to start a participant's clock.
		participant, err = s.registrations.Start(ctx, participant.ID, s.now())
		if err != nil {
			return SubmitOutcome{}, fmt.Errorf("start the participant's clock: %w", err)
		}
	}

	deadline, ok := Deadline(cmd.Contest, participant)
	if !ok {
		return SubmitOutcome{}, ErrDeadlinePassed
	}

	var (
		result  Submission
		correct bool
	)
	for attempt := 0; ; attempt++ {
		result, correct, err = s.submitOnce(ctx, participant.ID, q, cmd.Value, deadline)
		if !errors.Is(err, ErrAttemptConflict) {
			break
		}
		if attempt >= maxAttemptRetries {
			return SubmitOutcome{}, fmt.Errorf(
				"record the answer: lost the attempt race %d times in a row: %w", maxAttemptRetries, err)
		}
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

// submitOnce is one attempt at writing the answer: the core database's own
// clock, the deadline check against it, grading, the insert and — only when
// this attempt is newly correct — the score update, all in the one
// transaction §8 requires. Called more than once only when Insert reports
// ErrAttemptConflict (finding 3), in which case nothing here has taken
// effect (the transaction never committed) and Submit calls it again.
func (s *Service) submitOnce(ctx context.Context, registrationID uuid.UUID, q Question, value string, deadline time.Time) (Submission, bool, error) {
	var (
		result  Submission
		correct bool
	)
	err := s.uow.Do(ctx, func(ctx context.Context) error {
		now, err := s.submissions.Now(ctx)
		if err != nil {
			return fmt.Errorf("read the core database's clock: %w", err)
		}
		// The core database's own clock against the one deadline formula in
		// this codebase (§8) — checked here, inside the write's own
		// transaction, so the guarantee does not depend on the scheduler
		// having moved the contest to "finished", or on the application
		// server's clock agreeing with the database's.
		if !now.Before(deadline.Add(s.grace)) {
			return ErrDeadlinePassed
		}

		correct = s.grade(ctx, q, value)
		// Scoring is currently just the question's own nominal value on a
		// correct answer, nothing on a wrong one — §6.1.1's planned penalty
		// (a percentage taken per wrong attempt, floored at zero) is
		// deliberately not built here; this is the one place it plugs in,
		// against q and the attempt just graded, still inside this same
		// transaction and still written once to points_awarded.
		points := 0
		if correct {
			points = q.Points
		}

		result, err = s.submissions.Insert(ctx, SubmissionRequest{
			RegistrationID: registrationID,
			QuestionID:     q.ID,
			Value:          value,
			IsCorrect:      correct,
			PointsAwarded:  points,
			SubmittedAt:    now,
			MaxAttempts:    q.MaxAttempts,
		})
		if err != nil {
			return err
		}

		// Only when this attempt actually earns something: a middleware
		// write needs a reason (CLAUDE.md rule 6), and the same discipline
		// applies to any write on this hot path — an incorrect attempt, the
		// common case, must not pay for a score update that would add zero.
		if points > 0 {
			if err := s.registrations.AddScore(ctx, registrationID, points); err != nil {
				return err
			}
		}
		return nil
	})
	return result, correct, err
}

// matchAnswer reports whether value satisfies one reference answer, by that
// answer's own match_kind (§6).
//
// Go's regexp package is RE2: matching runs in time linear in the length of
// the input, with no backtracking construction to blow up on an adversarial
// value, so match_kind = regex needs no bound of its own beyond the one
// maxAnswerRunes already puts on value.
func matchAnswer(a Answer, value string) (bool, error) {
	switch a.MatchKind {
	case MatchExact:
		return value == a.Value, nil
	case MatchExactCI:
		return strings.EqualFold(value, a.Value), nil
	case MatchRegex:
		re, err := regexp.Compile(a.Value)
		if err != nil {
			return false, err
		}
		return re.MatchString(value), nil
	default:
		// Answer.Validate refuses every match_kind but the three above
		// before a row is ever stored; an unrecognised one here can only mean
		// data written some other way, and "does not match" is the safe
		// reading of it — the alternative, treating an unrecognised rule as
		// automatically satisfied, would score a row nobody validated.
		return false, nil
	}
}

// grade compares value against every reference answer of q, correct if any
// one of them matches — several rows per question is how spelling variants
// are handled (Answer's own doc in question.go).
//
// Every regex here was already compiled once, successfully, at authoring
// time (Answer.Validate, question.go) before it was ever stored, and
// Contest.ContentEditable forbids touching a question's answers once the
// contest is running — so the one case a compiled-pattern cache would save
// work in (many students, one question, one running contest) is exactly the
// case where the pattern is guaranteed not to change underneath it. A cache
// would only add a second place answers could go stale for zero benefit in
// the case that matters, so there is none: a question carries a handful of
// short, staff-written reference answers, and recompiling all of them is
// microseconds next to the several database round trips one submission
// already pays.
//
// A pattern that still fails to compile here — defence in depth, not an
// expected path, since Validate already rejected this at authoring time — is
// treated as never matching rather than failing the request: a broken row
// must not turn into a 500 for every participant who reaches that question,
// or make one student's failing grade come back differently (in status, in
// body, or in the time taken) from another's depending on whether they
// happened to hit it. It is logged by question and answer identifier only,
// never by the pattern itself — the log is read by staff, and the whole
// point of never returning a reference answer to a participant would be
// undone by printing it to a log they read.
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
