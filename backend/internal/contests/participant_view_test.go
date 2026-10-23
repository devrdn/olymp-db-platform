package contests_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/contests/conteststest"
	"github.com/google/uuid"
)

// newReader derives attempt stats and the sequential gate from the one
// submission store it returns, as production does from the submissions table.
func newReader() (*contests.Reader, *conteststest.Stories, *conteststest.Questions, *conteststest.Submissions) {
	stories := conteststest.NewStories()
	questions := conteststest.NewQuestions()
	submissions := conteststest.NewSubmissions()
	submissions.Clock = func() time.Time { return conteststest.FixtureNow }
	attempts := conteststest.NewAttempts(submissions)
	sequence := conteststest.NewSequentialProgress(questions, submissions)
	return contests.NewReader(stories, questions, attempts, sequence), stories, questions, submissions
}

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

// The publish gate makes this unreachable while running; the read must still
// refuse rather than show an empty story.
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

// §6.1.
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

// ParticipantQuestion has no field for reference answers; that, not a filter,
// is the guarantee.
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
	// Do not add an Answers field to ParticipantQuestion.
}

func TestQuestionsResolvesTheWordingToTheRequestedLanguage(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	questions.Put(contests.Question{
		ContestID: contestID, Ord: 1, Kind: contests.KindChoice, Points: 10, IsVisible: true,
		ChoiceIDs: []string{"a", "b"},
		Texts: map[string]contests.QuestionText{
			"en": {Title: "The culprit", BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
			"ru": {Title: "Виновник", BodyMD: "Кто это сделал?", Choices: map[string]string{"a": "Дворецкий", "b": "Садовник"}},
		},
	})

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "ru", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 1 || found[0].Title != "Виновник" || found[0].BodyMD != "Кто это сделал?" || found[0].Choices["a"] != "Дворецкий" {
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
	// No entry for unlimited must read as "never attempted", not an error.

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

// A reloaded screen needs these to tell "solved" from "out of attempts".
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

	// Solved on the second attempt (10 less a penalty of 2); the other spent.
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

// §6.1.1: closed means answered correctly or out of attempts.
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
	if found[0].AttemptsRemaining == nil || *found[0].AttemptsRemaining != 0 {
		t.Fatalf("attempts remaining = %v, want 0", found[0].AttemptsRemaining)
	}
}

// Unreachable while running (publish gate), but the reader still drops it
// rather than serve an empty body, as Story does with ErrStoryNotFound.
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

// Without a sequential frontier, CanAnswer is exactly !Closed.
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

// sequentialQuestions stages three questions of a sequential contest, each with
// every kind of content a locked entry must not carry: a title, a body, choice
// labels and points.
func sequentialQuestions(questions *conteststest.Questions, contestID uuid.UUID, maxAttempts int) []contests.Question {
	staged := make([]contests.Question, 0, 3)
	for i, word := range []string{"culprit", "weapon", "room"} {
		staged = append(staged, questions.Put(contests.Question{
			ContestID: contestID, Ord: i + 1, Kind: contests.KindChoice, Points: 10 * (i + 1), IsVisible: true,
			MaxAttempts: &maxAttempts, ChoiceIDs: []string{"a", "b"},
			Texts: map[string]contests.QuestionText{"en": {
				Title: "The " + word, BodyMD: "Which " + word + "?",
				Choices: map[string]string{"a": "First " + word, "b": "Second " + word},
			}},
		}))
	}
	return staged
}

// lockedAt is the whole of what a participant learns about a question their
// sequential contest has not reached (§6.1.1).
func lockedAt(position int) contests.ParticipantQuestion {
	return contests.ParticipantQuestion{Position: position, Locked: true}
}

// expectOpen fails unless got is q's full content at position, answerable or
// not as canAnswer says.
func expectOpen(t *testing.T, got contests.ParticipantQuestion, q contests.Question, position int, canAnswer bool) {
	t.Helper()
	text := q.Texts["en"]
	if got.Locked || got.Position != position || got.ID != q.ID || got.Title != text.Title ||
		got.BodyMD != text.BodyMD || got.Points != q.Points || !reflect.DeepEqual(got.Choices, text.Choices) ||
		!reflect.DeepEqual(got.ChoiceIDs, q.ChoiceIDs) || got.CanAnswer != canAnswer {
		t.Errorf("question %d = %+v, want its full content with CanAnswer %v", position, got, canAnswer)
	}
}

// §6.1.1: past the frontier the server withholds everything but the number.
func TestQuestionsWithholdsEveryQuestionPastTheSequentialFrontier(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	staged := sequentialQuestions(questions, contestID, 2)

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("got %d questions, want 3 (a locked question is listed, by number)", len(found))
	}
	expectOpen(t, found[0], staged[0], 1, true)
	for i, got := range found[1:] {
		if want := lockedAt(i + 2); !reflect.DeepEqual(got, want) {
			t.Errorf("question %d = %+v, want %+v and nothing else", i+2, got, want)
		}
	}
	// Belt and braces: the formatted list names nothing past the first.
	if dump := fmt.Sprintf("%+v", found[1:]); strings.Contains(dump, "weapon") || strings.Contains(dump, "room") {
		t.Errorf("a locked question's wording reached the list: %s", dump)
	}
}

func TestQuestionsOpensTheNextQuestionOnceTheCurrentOneIsAnswered(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	staged := sequentialQuestions(questions, contestID, 2)

	submit(t, submissions, registrationID, staged[0], true, 0)

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	expectOpen(t, found[0], staged[0], 1, false)
	if !found[0].Closed || !found[0].Correct {
		t.Errorf("the answered question = %+v, want it closed and correct, shown in full", found[0])
	}
	expectOpen(t, found[1], staged[1], 2, true)
	if !reflect.DeepEqual(found[2], lockedAt(3)) {
		t.Errorf("question 3 = %+v, want it still locked", found[2])
	}
}

func TestQuestionsOpensTheNextQuestionOnceTheCurrentOneRunsOutOfAttempts(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	staged := sequentialQuestions(questions, contestID, 2)

	submit(t, submissions, registrationID, staged[0], false, 0)
	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	expectOpen(t, found[0], staged[0], 1, true)
	if !reflect.DeepEqual(found[1], lockedAt(2)) {
		t.Fatalf("question 2 = %+v, want it locked while one attempt on question 1 is left", found[1])
	}

	submit(t, submissions, registrationID, staged[0], false, 0)
	found, err = reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	expectOpen(t, found[0], staged[0], 1, false)
	if !found[0].Closed || found[0].Correct {
		t.Errorf("the spent question = %+v, want it closed and not correct, shown in full", found[0])
	}
	expectOpen(t, found[1], staged[1], 2, true)
	if !reflect.DeepEqual(found[2], lockedAt(3)) {
		t.Errorf("question 3 = %+v, want it still locked", found[2])
	}
}

// Every closed question is shown in full wherever it stands, even past the
// frontier, since what the participant answered is already theirs.
func TestQuestionsShowsAClosedQuestionPastTheFrontierInFull(t *testing.T) {
	reader, _, questions, submissions := newReader()
	contestID := uuid.New()
	registrationID := uuid.New()
	staged := sequentialQuestions(questions, contestID, 2)

	submit(t, submissions, registrationID, staged[2], true, 0)

	found, err := reader.Questions(t.Context(), contestID, registrationID, "en", true)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	expectOpen(t, found[0], staged[0], 1, true)
	if !reflect.DeepEqual(found[1], lockedAt(2)) {
		t.Errorf("question 2 = %+v, want it locked", found[1])
	}
	expectOpen(t, found[2], staged[2], 3, false)
}

// Free progression locks nothing: every question is listed in full.
func TestQuestionsLocksNothingUnderFreeProgression(t *testing.T) {
	reader, _, questions, _ := newReader()
	contestID := uuid.New()
	staged := sequentialQuestions(questions, contestID, 2)

	found, err := reader.Questions(t.Context(), contestID, uuid.New(), "en", false)
	if err != nil {
		t.Fatalf("Questions() = %v", err)
	}
	if len(found) != len(staged) {
		t.Fatalf("got %d questions, want %d", len(found), len(staged))
	}
	for i, q := range staged {
		expectOpen(t, found[i], q, i+1, true)
	}
}
