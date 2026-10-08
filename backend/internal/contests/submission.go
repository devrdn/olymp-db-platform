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
	// they already answered it correctly, or every attempt is spent. "Not
	// shown" and "not answerable" are different decisions (§6.1) — this is
	// the second one, and it applies to a hidden question exactly as it does
	// to a visible one, since Submit never consults IsVisible at all.
	//
	// An answer that arrives after the participant's own deadline is refused
	// with ErrDeadlinePassed instead, declared beside the participation gate
	// (standing.go), because a participant whose time is up meets it there
	// first.
	ErrQuestionClosed = errors.New("this question is closed")
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
	// ErrNotAChoice is a value for a choice question that is not exactly one
	// of its option identifiers. A choice question is answered by picking an
	// option; grading free text against it would let a string that matches
	// several options at once (an unanchored pattern, say) solve the question
	// without choosing. Refused before grading and before any write, so it
	// costs no attempt.
	ErrNotAChoice = errors.New("the answer is not one of the question's options")
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
//
// The jitter is scheduling, not secrecy. It decides no access, mints no token
// and protects nothing a caller could gain by predicting it: somebody who knew
// the delay exactly could at best arrange to collide on purpose, which they can
// already do by submitting at the same moment, and which the unique constraint
// on (registration_id, question_id, attempt_no) settles either way. A
// cryptographic source here would buy nothing and can fail, which a retry delay
// must not.
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
	// the retry loop starts, by the same closesAt the participation gate
	// refuses at) — the instant at or after which Insert must
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
// Participant and Contest are trusted as already resolved by the caller: the
// answer route resolves them through queryproxy.Service.Access, which admits
// them through the participation gate (StandingOf) before the body is even
// read. Submit asks the same gate again, from Address — not a second rule,
// the same function — because Submit is the method that writes, and nothing
// stops another caller from reaching it without the route's admission.
// contests cannot import queryproxy to call Access itself either way —
// queryproxy is built on top of this package, not the other way round.
//
// What Submit adds on top, and the gate could never answer on its own:
// whether the question actually belongs to this contest, whether this
// registration may still answer it at all, and whether the deadline has
// passed by the core database's own clock (§8) at the moment of the write
// rather than the moment the gate was asked.
type SubmitCommand struct {
	Participant Participant
	Contest     Contest
	QuestionID  uuid.UUID
	Value       string
	// Address is where the answer came from, for the contest's network
	// restriction. The zero value is an address nobody could name, which a
	// restricted contest refuses (Contest.AllowsAddress).
	Address netip.Addr
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
	// The participation gate first, before the question is read or a clock
	// started: a participant who may not act learns nothing about which
	// questions exist, and a refused answer starts nothing.
	if err := StandingOf(cmd.Contest, cmd.Participant, s.now(), s.grace, cmd.Address).Refusal(); err != nil {
		return SubmitOutcome{}, err
	}
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

	// A choice question is answered by picking one of its options, and only
	// an option is graded: free text would be matched against the reference
	// answers as-is, and an unanchored pattern accepting option "b" would
	// accept "abc" too, solving the question without choosing. Refused here,
	// before the clock is started, before grading and before any write, so a
	// refused value costs no attempt. The caller's rate budget was already
	// spent on the way in (CLAUDE.md rule 13).
	if q.Kind == KindChoice && !q.HasChoice(cmd.Value) {
		return SubmitOutcome{}, ErrNotAChoice
	}

	participant, err := s.startClock(ctx, cmd.Contest, cmd.Participant, cmd.Address)
	if err != nil {
		return SubmitOutcome{}, err
	}

	// The instant the write must refuse at is the one the gate refuses at:
	// the participant's own deadline plus the grace, from the same helper
	// (closesAt), worked out once rather than inside every retry — the grace
	// is a fixed installation setting, not something that could change
	// between tries.
	//
	// The gate has just admitted this participant, so a deadline is there to
	// compute — except for one it admitted to start whose clock Start left
	// pending: a Start that reported success without starting anything broke
	// its own contract, and a pending clock has no deadline (Deadline). Taken
	// at its word, the gate would let them start forever with no deadline
	// running; this fails closed instead, as the gate does on any
	// registration no deadline can be computed for.
	deadline, ok := Deadline(cmd.Contest, participant)
	if !ok {
		return SubmitOutcome{}, ErrContestNotRunning
	}
	writeDeadline := closesAt(deadline, s.grace)

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

// startClock starts an individual participant's own clock on their first
// answer, and asks the gate again about the participant that start handed
// back. Anybody whose clock is already running, or who has none of their
// own, is returned as they are: the gate has already admitted them.
//
// The same seam queryproxy uses for the identical decision (§8, finding 2):
// the registration's own Start, which can only ever set started_at once
// (postgres.Registrations.Start). Nothing here invents a second way to start
// a participant's clock. The first gate let them start inside the contest's
// window; the second decides whether the participant they now are may act:
// the start another request made first, whose time may already be up, or a
// registration disqualified since, which Start does not move. A start that
// leaves no deadline to compute is refused by Submit before the write.
func (s *Service) startClock(ctx context.Context, c Contest, p Participant, addr netip.Addr) (Participant, error) {
	if !ClockPending(c, p) {
		return p, nil
	}
	started, err := s.registrations.Start(ctx, p.ID, s.now())
	if err != nil {
		return Participant{}, fmt.Errorf("start the participant's clock: %w", err)
	}
	if err := StandingOf(c, started, s.now(), s.grace, addr).Refusal(); err != nil {
		return Participant{}, err
	}
	return started, nil
}

// penaltyAmount is how many points one wrong attempt costs against q's own
// face value (§6.1.1): a percentage of q.Points, floored to an integer, or
// zero outright once c.Scoring says points are not the result — the penalty
// is defined in points, and stops meaning anything once points stop being
// what a result is. Not a flat refusal by configuration: a contest's scoring
// mode may change, and the percentage an organizer set must still be there,
// unapplied, if it changes back.
func penaltyAmount(q Question, c Contest) int {
	if c.Scoring == ScoringWinner || c.Scoring == ScoringICPC {
		return 0
	}
	return q.Points * q.PenaltyPct / 100
}

// awardablePoints is the face value fed to Insert for it to compute
// points_awarded from: q.Points in every mode but ICPC, where a question
// carries no points at all (design doc: place is decided by how many
// questions are solved and by penalty time, never by points) and every
// submission must write points_awarded = 0 so total_score stays 0 too —
// whether the answer is correct or not, and regardless of q.Points, exactly
// as if the question were configured worth nothing. Not a flat refusal by
// configuration, for the same reason penaltyAmount is not: a contest's
// scoring mode may change, and the points an organizer set on a question
// must still be there, unapplied, if it changes back to points or winner.
func awardablePoints(q Question, c Contest) int {
	if c.Scoring == ScoringICPC {
		return 0
	}
	return q.Points
}

// submitOnce writes one attempt: the deadline check, the attempt-number
// arithmetic and the penalty computation are all Insert's own single
// statement (§8, finding 5; §6.1.1) — called more than once only when Insert
// reports ErrAttemptConflict (finding 3), in which case nothing here has
// taken effect and Submit calls it again.
//
// A unit of work wraps the write whenever a correct answer could possibly
// earn something — points > 0 — because how much it actually earns is not
// known until Insert computes it from however many wrong attempts already
// landed; that amount might still turn out to be zero (the penalty already
// exhausted the question, §6.1.1's own floor), in which case the score update
// is skipped inside the same transaction rather than writing a zero delta
// (CLAUDE.md rule 6). A wrong answer, or points already zero — a question
// worth nothing to begin with, or ICPC scoring zeroing every question
// outright (awardablePoints) — needs no transaction at all — points_awarded
// is provably zero either way without asking the database anything — and
// opening one anyway would hold a pooled connection for a second round trip
// (COMMIT) that changes nothing.
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
// A pattern describes the whole answer (compileAnswerPattern): it is matched
// against the value with surrounding whitespace trimmed, since the anchors
// would otherwise make a stray space or a trailing newline from a pasted value
// the difference between right and wrong. Only the ends are trimmed; nothing
// inside the value is normalised.
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
		re, err := compileAnswerPattern(a.Value)
		if err != nil {
			return false, err
		}
		return re.MatchString(strings.TrimSpace(value)), nil
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
