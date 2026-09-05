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
// nothing to remember to filter (§6.1).
//
// Reader answers only "what may be shown"; whether the caller may ask at all
// — enrolled, the contest running, the address allowed — is
// queryproxy.Service.Access, checked by the caller before either method here
// is reached. Reader does not repeat that check: a second implementation of
// "may this student see this contest" is the bug this project keeps finding.
type Reader struct {
	stories   StoryRepository
	questions QuestionRepository
	attempts  AttemptStore
}

// NewReader assembles a participant-facing content reader.
func NewReader(stories StoryRepository, questions QuestionRepository, attempts AttemptStore) *Reader {
	return &Reader{stories: stories, questions: questions, attempts: attempts}
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
	story, err := r.stories.ByContest(ctx, contestID)
	if err != nil {
		return "", err
	}
	body, ok := story.Body(lang)
	if !ok {
		return "", ErrStoryNotFound
	}
	return body, nil
}

// ParticipantQuestion is one visible question as its participant sees it: its
// wording, its points, and where they stand on it — never a reference answer,
// never a penalty setting, never anything about anybody else's attempts.
type ParticipantQuestion struct {
	ID        uuid.UUID
	Ord       int
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
}

// Questions returns the contest's visible questions, in display order, with
// wording resolved to lang and this participant's own attempt state — never a
// hidden question (is_visible = false) and never a reference answer.
func (r *Reader) Questions(ctx context.Context, contestID, registrationID uuid.UUID, lang string) ([]ParticipantQuestion, error) {
	all, err := r.questions.List(ctx, contestID)
	if err != nil {
		return nil, err
	}
	stats, err := r.attempts.ForRegistration(ctx, registrationID)
	if err != nil {
		return nil, err
	}

	visible := make([]ParticipantQuestion, 0, len(all))
	for _, q := range all {
		if !q.IsVisible {
			continue
		}
		text := q.Texts[lang]
		used := stats[q.ID]
		visible = append(visible, ParticipantQuestion{
			ID:                q.ID,
			Ord:               q.Ord,
			Kind:              q.Kind,
			Points:            q.Points,
			ChoiceIDs:         q.ChoiceIDs,
			BodyMD:            text.BodyMD,
			Choices:           text.Choices,
			AttemptsRemaining: attemptsRemaining(q.MaxAttempts, used.Attempts),
			Closed:            isClosed(q.MaxAttempts, used),
		})
	}
	return visible, nil
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
