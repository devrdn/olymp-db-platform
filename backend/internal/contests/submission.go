package contests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
	// clock inside the same statement as the write (§8) — a second,
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
	// caller of Submit should never see it returned bare (see
	// ErrTooManyAttemptConflicts for what a caller does see once retrying
	// stops helping).
	ErrAttemptConflict = errors.New("lost the race for this attempt number")
	// ErrTooManyAttemptConflicts is what a caller of Submit actually sees
	// once every retry has lost the same race (finding 1): unlike
	// ErrAttemptConflict, this one is participant-facing and carries a
	// mapping in the HTTP layer, because running out of retries is not this
	// installation failing — it is a specific, honest fact about the
	// request ("too many people answered this exact question at this exact
	// moment; try again"), and the participant can act on it by resubmitting
	// rather than reading an internal error.
	ErrTooManyAttemptConflicts = errors.New("too many concurrent submissions to this question; try again")
	// ErrQuestionNotOpen is a question this registration may not answer yet:
	// the contest's progression is sequential (§6.1.1) and a question ordered
	// before this one is not closed — not answered correctly, and not out of
	// attempts either. Distinct from ErrQuestionClosed, which is the opposite
	// end of a question's life (nothing more to submit); this one names a
	// question that was never reachable in the first place. The server
	// checks this, not the interface: hiding an unopened question in the UI
	// is not what stops a direct request from answering it out of order.
	ErrQuestionNotOpen = errors.New("this question has not opened yet")
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
// submissions to the very same question by the very same registration —
// ordinarily the only thing that ever produces this conflict, since every
// other submission this registration makes targets a different question or
// arrives after the first has already committed. It also bounds what
// genuinely simultaneous submissions cost: concurrency here is strictly
// serialised by the table's own unique constraint, one winner per round, so
// the k-th of k truly simultaneous answers needs up to k tries — beyond this
// bound, Submit gives up rather than spinning (and rather than costing the
// database one more blocked transaction on the very tuple everyone is
// contending for) and returns ErrTooManyAttemptConflicts instead.
const maxAttemptRetries = 5

// attemptBackoffBase is the smallest delay Submit waits before retrying a
// lost attempt-number race, doubled each further retry and randomised by
// half (see attemptBackoff): small enough that a genuine one-off conflict is
// barely noticeable, and large enough that a burst of truly simultaneous
// submitters spreads its retries across time instead of hammering the same
// row again in lockstep on every single round (finding 1).
const attemptBackoffBase = 4 * time.Millisecond

// attemptBackoff is how long Submit waits before retrying the attempt-number
// race for the (attempt+1)-th time: an exponentially growing base, jittered
// by up to half so that several submitters who lost the same round do not
// all wake up and collide again at the same instant.
func attemptBackoff(attempt int) time.Duration {
	base := attemptBackoffBase << uint(attempt) // #nosec G115 -- attempt is bounded by maxAttemptRetries
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
	// Points is the question's own face value — what a correct answer is
	// worth with no wrong attempts behind it. Not what gets written to
	// points_awarded: Insert computes the actual amount itself (Points minus
	// PenaltyPerAttempt times however many attempts are already committed,
	// floored at zero, §6.1.1), atomically with the same count it uses to
	// assign the attempt number, so the two can never disagree about how
	// many attempts came before this one.
	Points int
	// PenaltyPerAttempt is how many points each already-committed wrong
	// attempt costs against Points — precomputed once by Service.Submit from
	// the question's own penalty_pct (zero when the contest ignores it, see
	// Contest.Scoring) — never a percentage carried into the statement for
	// Insert to multiply out itself, since the multiplication has nothing to
	// do with the race the statement exists to close.
	PenaltyPerAttempt int
	// Deadline is this participant's own deadline, grace already added
	// (contests.Deadline plus Service.grace, summed once by Submit before
	// the retry loop starts) — the instant at or after which Insert must
	// refuse the write regardless of attempts remaining. Checked by the
	// implementation against its own clock at the moment it actually writes
	// the row, not against a value Submit read earlier (finding 4, finding
	// 5): the guarantee section 8 asks for is that the clock, the deadline
	// and the row that depends on them cannot disagree, and folding the
	// check into the same operation that writes submitted_at is what makes
	// that true by construction rather than by two statements agreeing.
	Deadline time.Time
	// MaxAttempts is nil for a question with no cap.
	MaxAttempts *int
}

// SubmissionRepository records participants' answers.
//
// Deliberately one operation and no more: a count-then-check-then-insert
// repository is exactly the read-then-write shape the race (finding 3) asks
// this not to be, and a separate clock read ahead of the write is exactly the
// extra round trip and the extra staleness (finding 4, finding 5) folding the
// deadline into Insert removes. Everything Submit needs to decide and write
// an answer happens inside Insert's own statement instead.
type SubmissionRepository interface {
	// Insert writes one submission row, computing its own attempt number from
	// this registration/question's own history and checking req.Deadline
	// against its own clock, both in the same statement rather than reading
	// either first (finding 3, finding 4, finding 5): if the rows already
	// committed for this registration and question show it answered
	// correctly already, or every attempt already spent against
	// req.MaxAttempts, or the clock has reached req.Deadline, nothing is
	// inserted and ErrQuestionClosed or ErrDeadlinePassed is returned
	// (deadline takes priority when both apply, matching the order this
	// codebase checked them in before they were folded into one statement).
	// If two calls truly overlap — both reading a snapshot before either has
	// committed — and compute the same next attempt number, the table's own
	// UNIQUE (registration_id, question_id, attempt_no) lets only one land;
	// the other gets ErrAttemptConflict and Submit retries it from a fresh
	// read (see Service.Submit) rather than this method ever reading the
	// count and then writing it. The same already-committed count also
	// decides points_awarded when req.IsCorrect (§6.1.1): req.Points minus
	// req.PenaltyPerAttempt times the number of attempts already committed,
	// floored at zero — computed from the identical snapshot the attempt
	// number comes from, so a penalty can never be based on a count that
	// disagrees with the attempt number this same row is given.
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

// Submit records one participant's answer to one question, grades it and, if
// it is worth anything, updates their score.
//
// Grading happens once, before the write is ever attempted, and is never
// repeated across a retry: it is pure CPU over q.Answers, decided entirely by
// q and cmd.Value, neither of which a retry changes, so recomputing it on
// every one of maxAttemptRetries tries would recompile every reference
// pattern again for a race that has nothing to do with grading (finding 5).
// What a correct answer is actually worth is not decided here, though: the
// penalty (§6.1.1) depends on how many wrong attempts already landed, and
// that count is only known once Insert reads its own committed rows, so
// Submit hands Insert the question's face value and its per-attempt penalty
// and lets it work out the final number atomically, the same way it already
// works out the attempt number.
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

	// §6.1.1: in a sequential contest, this question may only be answered
	// once every question ordered before it is closed. Consulted only when
	// SequentialActive says progression is actually in effect (sequential
	// means nothing at single — the one question has nothing before it) — a
	// contest that never turns this on pays no extra round trip for it.
	if cmd.Contest.SequentialActive() {
		open, err := s.sequence.Open(ctx, cmd.Contest.ID, cmd.Participant.ID, q.Ord)
		if err != nil {
			return SubmitOutcome{}, fmt.Errorf("check whether question %s has opened: %w", q.ID, err)
		}
		if !open {
			return SubmitOutcome{}, ErrQuestionNotOpen
		}
	}

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
	// Grace added once, here, rather than inside every retry: it is a fixed
	// installation setting, not something that could change between tries.
	deadlineWithGrace := deadline.Add(s.grace)

	correct := s.grade(ctx, q, cmd.Value)
	penaltyPerAttempt := penaltyAmount(q, cmd.Contest)

	var result Submission
	for attempt := 0; ; attempt++ {
		result, err = s.submitOnce(ctx, participant.ID, q, cmd.Value, correct, penaltyPerAttempt, deadlineWithGrace)
		if !errors.Is(err, ErrAttemptConflict) {
			break
		}
		if attempt >= maxAttemptRetries {
			return SubmitOutcome{}, fmt.Errorf("%w: lost the attempt race %d times in a row: %w",
				ErrTooManyAttemptConflicts, maxAttemptRetries+1, err)
		}
		// A refused attempt still cost a round trip; waiting a little before
		// the next one keeps a burst of simultaneous losers from retrying in
		// lockstep and colliding again immediately (finding 1).
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

// penaltyAmount is how many points one wrong attempt costs against q's own
// face value (§6.1.1): a percentage of q.Points, floored to an integer, or
// zero outright once c.Scoring says points are not the result — the penalty
// is defined in points, and stops meaning anything once points stop being
// what a result is. Not a flat refusal by configuration: a contest's scoring
// mode may change, and the percentage an organizer set must still be there,
// unapplied, if it changes back.
func penaltyAmount(q Question, c Contest) int {
	if c.Scoring == ScoringWinner {
		return 0
	}
	return q.Points * q.PenaltyPct / 100
}

// submitOnce writes one attempt: the deadline check, the attempt-number
// arithmetic and the penalty computation are all Insert's own single
// statement (§8, finding 5; §6.1.1) — called more than once only when Insert
// reports ErrAttemptConflict (finding 3), in which case nothing here has
// taken effect and Submit calls it again.
//
// A unit of work wraps the write whenever a correct answer could possibly
// earn something — q.Points > 0 — because how much it actually earns is not
// known until Insert computes it from however many wrong attempts already
// landed; that amount might still turn out to be zero (the penalty already
// exhausted the question, §6.1.1's own floor), in which case the score update
// is skipped inside the same transaction rather than writing a zero delta
// (CLAUDE.md rule 6). A wrong answer, or a question worth zero points to
// begin with, needs no transaction at all — points_awarded is provably zero
// either way without asking the database anything — and opening one anyway
// would hold a pooled connection for a second round trip (COMMIT) that
// changes nothing.
func (s *Service) submitOnce(ctx context.Context, registrationID uuid.UUID, q Question, value string, correct bool, penaltyPerAttempt int, deadline time.Time) (Submission, error) {
	req := SubmissionRequest{
		RegistrationID:    registrationID,
		QuestionID:        q.ID,
		Value:             value,
		IsCorrect:         correct,
		Points:            q.Points,
		PenaltyPerAttempt: penaltyPerAttempt,
		Deadline:          deadline,
		MaxAttempts:       q.MaxAttempts,
	}

	if !correct || q.Points <= 0 {
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
			// The penalty already consumed the whole face value before this
			// attempt landed (§6.1.1's floor): nothing observable changes,
			// and a write with nothing to show for it is the one CLAUDE.md
			// rule 6 asks skipped.
			return nil
		}
		return s.registrations.AddScore(ctx, registrationID, result.PointsAwarded)
	})
	return result, err
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
// already pays — and, since finding 5, is done once per Submit rather than
// once per retry.
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
