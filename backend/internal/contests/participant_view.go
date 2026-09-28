package contests

import (
	"context"

	"github.com/google/uuid"
)

// Reader builds what a participant may see of a running contest: the story in
// one language, and the questions that are visible to them.
//
// This is a projection built for the purpose, not the authoring view
// (internal/api's staff-only ContestResponse/QuestionResponse) with fields
// stripped off. A hidden question is left out here by never being read into
// the result in the first place, and a reference answer is a field this type
// does not have — there is no "forgot to filter" here, because there is
// nothing to remember to filter (§6.1). questions is VisibleQuestionRepository,
// not QuestionRepository: a different query, built for this projection, not
// the staff one reused with a filter bolted on (see VisibleQuestionRepository).
//
// Reader answers only "what may be shown"; whether the caller may ask at all
// — enrolled, the contest running, the address allowed — is
// queryproxy.Service.Access, checked by the caller before either method here
// is reached. Reader does not repeat that check: a second implementation of
// "may this student see this contest" is the bug this project keeps finding.
type Reader struct {
	stories   StoryText
	questions VisibleQuestionRepository
	attempts  AttemptStore
	// sequence resolves which question is currently answerable under
	// sequential progression (finding 3). Optional at the type level for the
	// same reason Service's own field is (see ServiceConfig.Sequence's doc):
	// Questions only ever reads it when the contest it is asked about has
	// actually turned progression to sequential, so a caller with nothing to
	// do with that — or an installation that never turns it on — need not
	// supply one.
	sequence SequentialGate
}

// NewReader assembles a participant-facing content reader.
func NewReader(stories StoryText, questions VisibleQuestionRepository, attempts AttemptStore, sequence SequentialGate) *Reader {
	return &Reader{stories: stories, questions: questions, attempts: attempts, sequence: sequence}
}

// Story returns the contest's story in lang, or ErrStoryNotFound.
//
// lang is resolved by the caller (platform/i18n.Match, §6.2) against the
// languages the contest declares — Reader only ever answers for a language it
// is handed, so it stays free of everything about requests, headers or
// accounts. A contest missing a body for a language it declares should not
// happen once it is running (the publish gate requires a translation for
// every declared language before a contest may start), but a defensive read
// answers the same way a story that was never authored at all does, rather
// than showing an empty page as if it were the story.
func (r *Reader) Story(ctx context.Context, contestID uuid.UUID, lang string) (string, error) {
	return r.stories.BodyIn(ctx, contestID, lang)
}

// StoryText is the participant's read of a story: the one language they are
// shown, rather than the staff StoryRepository's every translation decoded to
// return one of them — the same split VisibleQuestionRepository makes for the
// questions.
type StoryText interface {
	// BodyIn returns the story's text in lang, or ErrStoryNotFound when the
	// contest has no story or no text in lang.
	BodyIn(ctx context.Context, contestID uuid.UUID, lang string) (string, error)
}

// ParticipantQuestion is one visible question as its participant sees it: its
// wording, its points, and where they stand on it — never a reference answer,
// never a penalty setting, never anything about anybody else's attempts.
//
// There is deliberately no display position on this type. The staff ordinal
// (Question.Ord) is dense across every question of a contest, visible ones
// included, so it counts what is hidden and says where: three visible
// questions numbered 1, 3, 4, 7 name exactly three hidden ones and place them
// precisely, which is the one fact §6.1 says a participant must work out
// rather than read off a field. The list this type comes back in already
// carries the only ordering a participant needs — Reader.Questions returns
// visible questions in display order — so there is nothing an ordinal field
// would add except that leak.
type ParticipantQuestion struct {
	ID        uuid.UUID
	Kind      string
	Points    int
	ChoiceIDs []string
	BodyMD    string
	// Choices maps a choice identifier to its label in the resolved language,
	// nil for a question that is not KindChoice.
	Choices map[string]string
	// AttemptsRemaining is nil when the question allows unlimited attempts.
	AttemptsRemaining *int
	// Closed reports whether the participant may no longer answer this
	// question: they already answered it correctly, or they used every
	// attempt they had. "Not shown" and "not answerable" are different
	// decisions (§6.1) — a hidden question stays answerable, and a visible
	// closed one stays visible, just with nothing left to submit.
	Closed bool
	// Correct reports whether one of this registration's own attempts on
	// this question was right. Meaningless — always false — while Closed is
	// false, since an unclosed question has not been won yet; the reason a
	// closed one carries this at all is completeness: a participant who
	// reloads the page has no other way to tell "closed because solved" from
	// "closed because every attempt is spent" (finding 5, docs/ARCHITECTURE.md).
	Correct bool
	// PointsAwarded is what this registration actually earned on this
	// question — 0 until Correct is true, the penalty-adjusted award
	// afterwards (AttemptStats.PointsAwarded's own doc). Never a reference
	// answer and never another participant's score; only this registration's
	// own, the same number a follow-up submission's own response would carry.
	PointsAwarded int
	// CanAnswer reports whether Submit would currently accept an answer for
	// this question. In free progression and single-question mode it is
	// simply !Closed — nothing else ever gates a submission there. In
	// sequential progression (§6.1.1) it is also false for every unclosed
	// question except the one lowest in display order: the same rule
	// Service.Submit itself enforces (ErrQuestionNotOpen), surfaced here so a
	// participant can tell which of several unclosed questions to work on
	// without probing each one and collecting refusals (finding 3).
	//
	// This never exposes a display position, only "yes" or "no" per question
	// already in the list — the same guarantee ParticipantQuestion's missing
	// ordinal field protects (see its own doc): a hidden question's existence
	// and place in the order still cannot be read off this field, since it
	// is computed from the same ord sequence SequentialGate already
	// consults, is_visible included, and never returned.
	CanAnswer bool
}

// VisibleQuestion is one question already filtered and resolved for a
// participant: is_visible, in one language, with no reference answer read at
// all — the projection VisibleQuestionRepository owns, before Reader adds
// this participant's own attempt state to it.
type VisibleQuestion struct {
	ID          uuid.UUID
	Kind        string
	Points      int
	MaxAttempts *int
	ChoiceIDs   []string
	BodyMD      string
	Choices     map[string]string
}

// VisibleQuestionRepository is the participant-facing read of a contest's
// questions — a different query from QuestionRepository.List, built for this
// projection rather than the staff one with a filter added on top. See
// QuestionRepository's own doc for why the two must not share an
// implementation.
type VisibleQuestionRepository interface {
	// ForContest returns the contest's questions that are visible
	// (is_visible) and have a translation in lang, in display order, with no
	// reference answer selected at all. A question without a body in lang is
	// left out rather than served with an empty one — the same defensive
	// choice Story makes with ErrStoryNotFound (§6.2), applied per question
	// since this is a list rather than one resource.
	ForContest(ctx context.Context, contestID uuid.UUID, lang string) ([]VisibleQuestion, error)
}

// Questions returns the contest's visible questions, in display order, with
// wording resolved to lang and this participant's own attempt state — never a
// hidden question (is_visible = false) and never a reference answer, because
// VisibleQuestionRepository never reads one into memory in the first place.
//
// sequential is the contest's own Contest.SequentialActive() — the same
// method Service.Submit calls to key its own order check on, so the two can
// no longer read this rule differently (finding 4, contests.go's own doc on
// the method). Reader takes it as given rather than loading the contest
// itself, since the caller (the participant handler) already has it from the
// same Access call that admitted the request. It costs one extra round trip,
// to r.sequence, and only when sequential is true: free progression and
// single-question mode have nothing this could add, since every unclosed
// question is already answerable there (finding 3).
func (r *Reader) Questions(ctx context.Context, contestID, registrationID uuid.UUID, lang string, sequential bool) ([]ParticipantQuestion, error) {
	visible, err := r.questions.ForContest(ctx, contestID, lang)
	if err != nil {
		return nil, err
	}
	stats, err := r.attempts.ForRegistration(ctx, registrationID)
	if err != nil {
		return nil, err
	}

	// The one question, if any, sequential progression currently allows an
	// answer for — the lowest in display order that is not yet closed,
	// hidden questions counted exactly as visible ones (§6.1.1), the same
	// rule postgres.Sequence.Open already enforces at Submit. Resolved once
	// here rather than per question: a per-question call would cost as many
	// round trips as this list has entries, for a fact that is the same
	// answer every time it is asked in the same request.
	var frontier uuid.UUID
	if sequential {
		frontier, err = r.sequence.Frontier(ctx, contestID, registrationID)
		if err != nil {
			return nil, err
		}
	}

	out := make([]ParticipantQuestion, 0, len(visible))
	for _, q := range visible {
		used := stats[q.ID]
		closed := isClosed(q.MaxAttempts, used)
		canAnswer := !closed
		if sequential {
			canAnswer = !closed && q.ID == frontier
		}
		out = append(out, ParticipantQuestion{
			ID:                q.ID,
			Kind:              q.Kind,
			Points:            q.Points,
			ChoiceIDs:         q.ChoiceIDs,
			BodyMD:            q.BodyMD,
			Choices:           q.Choices,
			AttemptsRemaining: attemptsRemaining(q.MaxAttempts, used.Attempts),
			Closed:            closed,
			CanAnswer:         canAnswer,
			Correct:           used.Correct,
			PointsAwarded:     used.PointsAwarded,
		})
	}
	return out, nil
}

// attemptsRemaining is nil for a question with no cap, and never negative —
// an attempt count fresher than the question's own setting (a penalty
// applied after the cap was lowered, say) must not read as a negative
// allowance.
func attemptsRemaining(max *int, used int) *int {
	if max == nil {
		return nil
	}
	remaining := *max - used
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

// isClosed reports whether nothing more may be submitted for a question: a
// correct answer closes it outright, and otherwise it closes once every
// attempt is spent — a question with no cap never closes this way.
func isClosed(max *int, used AttemptStats) bool {
	if used.Correct {
		return true
	}
	return max != nil && used.Attempts >= *max
}
