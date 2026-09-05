package contests_test

import (
	"context"
	"errors"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

func newReader() (*contests.Reader, *conteststest.Stories, *conteststest.Questions, *conteststest.Attempts) {
	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	attempts := conteststest.NewAttempts()
	return contests.NewReader(stories, questions, attempts), stories, questions, attempts
}

func TestStoryReturnsTheBodyInTheResolvedLanguage(t *testing.T) {
	reader, stories, _, _ := newReader()
	contestID := uuid.New()
	if _, err := stories.Save(context.Background(), contestID, map[string]string{
		"en": "A body in the stacks.",
		"ru": "Тело в архиве.",
	}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	body, err := reader.Story(t.Context(), contestID, "ru")
	if err != nil {
		t.Fatalf("Story() = %v", err)
	}
	if body != "Тело в архиве." {
		t.Fatalf("body = %q, want the Russian text", body)
	}
}

// The publish gate guarantees a body for every declared language while the
// contest is running, but a defensive read must still refuse honestly rather
// than show an empty page as if it were the story.
func TestStoryIsNotFoundWhenTheLanguageHasNoBody(t *testing.T) {
	reader, stories, _, _ := newReader()
	contestID := uuid.New()
	if _, err := stories.Save(context.Background(), contestID, map[string]string{"en": "A body in the stacks."}); err != nil {
		t.Fatalf("Save() = %v", err)
	}

	if _, err := reader.Story(t.Context(), contestID, "ro"); !errors.Is(err, contests.ErrStoryNotFound) {
		t.Fatalf("error = %v, want ErrStoryNotFound", err)
	}
}

func TestStoryIsNotFoundWhenTheContestHasNone(t *testing.T) {
	reader, _, _, _ := newReader()

	if _, err := reader.Story(t.Context(), uuid.New(), "en"); !errors.Is(err, contests.ErrStoryNotFound) {
		t.Fatalf("error = %v, want ErrStoryNotFound", err)
	}
}

// The one requirement section 6.1 is explicit about: a hidden question exists
// fully but never reaches the participant's list.
func TestQuestionsOmitsHiddenQuestions(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})
	hidden := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: false,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "candlestick"}},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("got %d questions, want 1 (the hidden one must be absent)", len(found))
	}
	for _, q := range found {
		if q.ID == hidden.ID {
			t.Fatalf("the hidden question was returned: %+v", q)
		}
	}
}

// The reference answer must never appear on the participant's own type: there
// is no field for it, which is the guarantee, not a filter that could be
// forgotten. This test proves the visible question's wording came through
// while nothing about answers did, by construction of ParticipantQuestion.
func TestQuestionsNeverCarryReferenceAnswers(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts:   map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || found[0].BodyMD != "Who did it?" {
		t.Fatalf("found = %+v", found)
	}
	// ParticipantQuestion has no Answers field at all — if this ever compiles
	// again with one added, the reviewer adding it should read this comment
	// before wiring reference answers into it.
}

func TestQuestionsResolvesTheWordingToTheRequestedLanguage(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindChoice, Points: 10, IsVisible: true,
		ChoiceIDs: []string{"a", "b"},
		Texts: map[string]contests.QuestionText{
			"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
			"ru": {BodyMD: "Кто это сделал?", Choices: map[string]string{"a": "Дворецкий", "b": "Садовник"}},
		},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "ru")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || found[0].BodyMD != "Кто это сделал?" || found[0].Choices["a"] != "Дворецкий" {
		t.Fatalf("found = %+v", found)
	}
}

func TestQuestionsReportsAttemptsRemainingAndClosed(t *testing.T) {
	reader, _, questions, attempts := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	maxAttempts := 3

	capped := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	unlimited := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
	})
	solved := questions.Put(contests.Question{
		ContestID: contestID, Ord: 3, Kind: contests.KindText, Points: 5, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Where?"}},
	})

	attempts.Put(registrationID, capped.ID, contests.AttemptStats{Attempts: 2})
	attempts.Put(registrationID, solved.ID, contests.AttemptStats{Attempts: 1, Correct: true})
	// unlimited and, implicitly, a fresh registration on capped: no entry at
	// all, which must read as "never attempted" rather than an error.

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	byID := map[uuid.UUID]contests.ParticipantQuestion{}
	for _, q := range found {
		byID[q.ID] = q
	}

	got := byID[capped.ID]
	if got.AttemptsRemaining == nil || *got.AttemptsRemaining != 1 {
		t.Fatalf("capped question attempts remaining = %v, want 1", got.AttemptsRemaining)
	}
	if got.Closed {
		t.Fatalf("capped question with attempts left reported closed")
	}

	got = byID[unlimited.ID]
	if got.AttemptsRemaining != nil {
		t.Fatalf("unlimited question attempts remaining = %v, want nil", got.AttemptsRemaining)
	}
	if got.Closed {
		t.Fatalf("unlimited, never-attempted question reported closed")
	}

	got = byID[solved.ID]
	if !got.Closed {
		t.Fatalf("a question already answered correctly must be closed")
	}
	if got.AttemptsRemaining == nil || *got.AttemptsRemaining != 2 {
		t.Fatalf("solved question attempts remaining = %v, want 2 (the cap minus the one spent attempt)", got.AttemptsRemaining)
	}
}

// A participant who exhausted every attempt without ever answering correctly
// is closed too — the other half of "closed" (§6.1.1: answered correctly or
// out of attempts).
func TestQuestionsClosesAQuestionOnceEveryAttemptIsSpent(t *testing.T) {
	reader, _, questions, attempts := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	maxAttempts := 2

	q := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	attempts.Put(registrationID, q.ID, contests.AttemptStats{Attempts: 2})

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || !found[0].Closed {
		t.Fatalf("found = %+v, want the question closed", found)
	}
	if *found[0].AttemptsRemaining != 0 {
		t.Fatalf("attempts remaining = %d, want 0", *found[0].AttemptsRemaining)
	}
}

func TestQuestionsIsEmptyForAContestWithNoVisibleQuestions(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Secret."}},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en")
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %+v, want none", found)
	}
}
