package contests_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// newReader assembles a Reader over in-memory stores. Its attempt stats and
// its sequential gate both derive from the one submission store it returns,
// as production derives both from the submissions table, so a test records
// what a participant tried the way Submit does — through Insert, once — and
// both read it back.
func newReader() (*contests.Reader, *conteststest.Stories, *conteststest.Questions, *conteststest.Submissions) {
	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	submissions := conteststest.NewSubmissions()
	submissions.Clock = func() time.Time { return conteststest.FixtureNow }
	attempts := conteststest.NewAttempts(submissions)
	sequence := conteststest.NewSequentialProgress(questions, submissions)
	return contests.NewReader(stories, questions, attempts, sequence), stories, questions, submissions
}

// submit records one answer through the submission store, with the question's
// own cap and a deadline still ahead of the store's clock.
func submit(t *testing.T, submissions *conteststest.Submissions, registrationID uuid.UUID, q contests.Question, correct bool, penalty int) {
	t.Helper()
	if _, err := submissions.Insert(t.Context(), contests.SubmissionRequest{
		RegistrationID: registrationID, QuestionID: q.ID, Value: "an answer",
		IsCorrect: correct, Points: q.Points, PenaltyPerAttempt: penalty,
		MaxAttempts: q.MaxAttempts, Deadline: conteststest.FixtureNow.Add(time.Hour),
	}); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
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

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", false)
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

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", false)
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

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "ru", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || found[0].BodyMD != "Кто это сделал?" || found[0].Choices["a"] != "Дворецкий" {
		t.Fatalf("found = %+v", found)
	}
}

func TestQuestionsReportsAttemptsRemainingAndClosed(t *testing.T) {
	reader, _, questions, submissions := newReader()
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

	submit(t, submissions, registrationID, capped, false, 0)
	submit(t, submissions, registrationID, capped, false, 0)
	submit(t, submissions, registrationID, solved, true, 0)
	// unlimited and, implicitly, a fresh registration on capped: no entry at
	// all, which must read as "never attempted" rather than an error.

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", false)
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

// Finding 5: a reloaded screen has no other way to tell "closed because
// solved" from "closed because every attempt is spent" — both looked like a
// bare "Closed." before Correct and PointsAwarded existed on this type.
func TestQuestionsReportsCorrectAndPointsAwarded(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	maxAttempts := 3

	solved := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	exhausted := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
	})
	untouched := questions.Put(contests.Question{
		ContestID: contestID, Ord: 3, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Where?"}},
	})

	// Won on the second attempt, at 10 points less one wrong attempt's
	// penalty of 2; and every attempt on the other one spent.
	submit(t, submissions, registrationID, solved, false, 2)
	submit(t, submissions, registrationID, solved, true, 2)
	for range maxAttempts {
		submit(t, submissions, registrationID, exhausted, false, 2)
	}

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	byID := map[uuid.UUID]contests.ParticipantQuestion{}
	for _, q := range found {
		byID[q.ID] = q
	}

	if got := byID[solved.ID]; !got.Correct || got.PointsAwarded != 8 {
		t.Fatalf("solved question = %+v, want {Correct: true, PointsAwarded: 8}", got)
	}
	if got := byID[exhausted.ID]; got.Correct || got.PointsAwarded != 0 {
		t.Fatalf("exhausted question = %+v, want {Correct: false, PointsAwarded: 0}", got)
	}
	if got := byID[untouched.ID]; got.Correct || got.PointsAwarded != 0 {
		t.Fatalf("untouched question = %+v, want {Correct: false, PointsAwarded: 0}", got)
	}
}

// A participant who exhausted every attempt without ever answering correctly
// is closed too — the other half of "closed" (§6.1.1: answered correctly or
// out of attempts).
func TestQuestionsClosesAQuestionOnceEveryAttemptIsSpent(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	maxAttempts := 2

	q := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	submit(t, submissions, registrationID, q, false, 0)
	submit(t, submissions, registrationID, q, false, 0)

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", false)
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

// Finding 5: a question missing the resolved language is left out of the
// list rather than served with an empty body — the same defensive choice
// Story makes with ErrStoryNotFound, applied per question because this is a
// list rather than one resource. The publish gate makes this unreachable for
// a running contest, but the reader must still answer this way rather than
// leak an empty-bodied entry.
func TestQuestionsOmitsAQuestionMissingTheResolvedLanguage(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	withEnglish := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts: map[string]contests.QuestionText{"ru": {BodyMD: "Кто это сделал?"}},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || found[0].ID != withEnglish.ID {
		t.Fatalf("found = %+v, want only the question with an English body", found)
	}
	if strings.Contains(strings.ToLower(fmt.Sprint(found)), "сделал") {
		t.Fatalf("the question with no English body still leaked its Russian one: %+v", found)
	}
}

func TestQuestionsIsEmptyForAContestWithNoVisibleQuestions(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, IsVisible: false,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Secret."}},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("found = %+v, want none", found)
	}
}

// Outside sequential progression nothing else gates a submission beyond
// "closed", so CanAnswer must simply agree with !Closed for every question —
// free progression and single-question mode have no frontier of their own
// (finding 3).
func TestQuestionsCanAnswerMatchesClosedOutsideSequentialProgression(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	maxAttempts := 1

	open := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	closed := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		MaxAttempts: &maxAttempts,
		Texts:       map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
	})
	submit(t, submissions, registrationID, closed, false, 0)

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	byID := map[uuid.UUID]contests.ParticipantQuestion{}
	for _, q := range found {
		byID[q.ID] = q
	}

	if !byID[open.ID].CanAnswer {
		t.Errorf("open question CanAnswer = false, want true")
	}
	if byID[closed.ID].CanAnswer {
		t.Errorf("closed question CanAnswer = true, want false")
	}
}

// §6.1.1, finding 3: sequential progression only ever lets one question be
// answered at a time — the one lowest in display order that is not yet
// closed. A participant looking at several unclosed questions must be able
// to tell which one that is without probing each and collecting refusals.
func TestQuestionsMarksOnlyTheSequentialFrontierAnswerable(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()

	first := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	second := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
	})

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	byID := map[uuid.UUID]contests.ParticipantQuestion{}
	for _, q := range found {
		byID[q.ID] = q
	}

	if !byID[first.ID].CanAnswer {
		t.Errorf("first question CanAnswer = false, want true (nothing has closed it yet)")
	}
	if byID[second.ID].CanAnswer {
		t.Errorf("second question CanAnswer = true, want false (the first has not closed)")
	}
}

// The frontier moves forward, by exactly one question, once the question
// holding it closes.
func TestQuestionsMovesTheSequentialFrontierOnceAQuestionCloses(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()

	first := questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindText, Points: 10, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "Who did it?"}},
	})
	second := questions.Put(contests.Question{
		ContestID: contestID, Ord: 2, Kind: contests.KindText, Points: 5, IsVisible: true,
		Texts: map[string]contests.QuestionText{"en": {BodyMD: "What weapon?"}},
	})

	// Close the first question with a correct answer, which both the attempt
	// stats Reader reads and the sequential gate derive from.
	submit(t, submissions, registrationID, first, true, 0)

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	byID := map[uuid.UUID]contests.ParticipantQuestion{}
	for _, q := range found {
		byID[q.ID] = q
	}

	if byID[first.ID].CanAnswer {
		t.Errorf("closed first question CanAnswer = true, want false")
	}
	if !byID[second.ID].CanAnswer {
		t.Errorf("second question CanAnswer = false, want true (the first is now closed)")
	}
}
