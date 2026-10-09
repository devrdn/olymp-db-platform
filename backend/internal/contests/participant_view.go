package contests

import (
	"context"

	"github.com/google/uuid"
)

// Reader builds what a participant may see of a running contest: the story in
// one language and the visible questions. It is a projection of its own, not
// the staff view with fields stripped, so hidden questions and reference
// answers are never read at all (§6.1).
//
// Whether the caller may ask is Gate.StandingOf's question, which the caller
// answers first; Reader does not repeat it.
type Reader struct {
	stories   StoryText
	questions VisibleQuestionRepository
	attempts  AttemptStore
	// sequence is read only for a sequential contest; optional otherwise.
	sequence SequentialGate
}

// NewReader assembles a participant-facing content reader.
func NewReader(stories StoryText, questions VisibleQuestionRepository, attempts AttemptStore, sequence SequentialGate) *Reader {
	return &Reader{stories: stories, questions: questions, attempts: attempts, sequence: sequence}
}

// Story returns the contest's story in lang, or ErrStoryNotFound, also when
// the text in lang is missing. The caller resolves lang (§6.2).
func (r *Reader) Story(ctx context.Context, contestID uuid.UUID, lang string) (string, error) {
	return r.stories.BodyIn(ctx, contestID, lang)
}

// StoryText is the participant's read of a story, in one language.
type StoryText interface {
	// BodyIn returns the story's text in lang, or ErrStoryNotFound when the
	// contest has no story or no text in lang.
	BodyIn(ctx context.Context, contestID uuid.UUID, lang string) (string, error)
}

// ParticipantQuestion is one visible question as its participant sees it,
// with their own standing on it.
//
// It has no display position: Question.Ord counts hidden questions too, so
// gaps would reveal them (§6.1). The list is already in display order.
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
	// Closed reports that the question was answered correctly or every
	// attempt is spent.
	Closed bool
	// Correct tells a closed question solved from one with attempts spent;
	// always false while not Closed.
	Correct bool
	// PointsAwarded is this registration's penalty-adjusted award, 0 until
	// Correct.
	PointsAwarded int
	// CanAnswer reports whether Submit would accept an answer now: !Closed,
	// and under sequential progression (§6.1.1) only for the frontier
	// question. It is a yes or no, never a position.
	CanAnswer bool
}

// VisibleQuestion is a visible question in one language, without reference
// answers, before Reader adds the participant's attempt state.
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
// questions, a separate query from QuestionRepository.List.
type VisibleQuestionRepository interface {
	// ForContest returns the visible questions with a body in lang, in
	// display order, never selecting reference answers.
	ForContest(ctx context.Context, contestID uuid.UUID, lang string) ([]VisibleQuestion, error)
}

// Questions returns the contest's visible questions in display order, in
// lang, with this participant's attempt state. sequential must be the
// contest's SequentialActive(), the same rule Submit checks.
func (r *Reader) Questions(ctx context.Context, contestID, registrationID uuid.UUID, lang string, sequential bool) ([]ParticipantQuestion, error) {
	visible, err := r.questions.ForContest(ctx, contestID, lang)
	if err != nil {
		return nil, err
	}
	stats, err := r.attempts.ForRegistration(ctx, registrationID)
	if err != nil {
		return nil, err
	}

	// The lowest unclosed question, hidden ones counted (§6.1.1), resolved
	// once rather than per question.
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

// attemptsRemaining is nil for a question with no cap, and never negative,
// since the cap may have been lowered after attempts were made.
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

// isClosed reports whether nothing more may be submitted for a question.
func isClosed(max *int, used AttemptStats) bool {
	if used.Correct {
		return true
	}
	return max != nil && used.Attempts >= *max
}
