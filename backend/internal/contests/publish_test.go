package contests_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

func publishable() (contests.Contest, contests.Story, []contests.Question) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(3 * time.Hour)

	c := contests.Contest{
		ID:           uuid.New(),
		Status:       contests.StatusDraft,
		Enrollment:   contests.EnrollmentInviteOnly,
		QuestionMode: contests.QuestionModeMulti,
		Timing:       contests.TimingFixed,
		StartsAt:     &start,
		EndsAt:       &end,
		Languages:    []contests.ContestLanguage{{Code: "en", IsDefault: true}, {Code: "ro"}},

		LeaderboardNames: contests.LeaderboardNamesLogin,
		Translations: map[string]contests.Translation{
			"en": {Lang: "en", Title: "The Library Murder"},
			"ro": {Lang: "ro", Title: "Crima din bibliotecă"},
		},
	}
	story := contests.Story{
		ID:        uuid.New(),
		ContestID: c.ID,
		Bodies:    map[string]string{"en": "A body in the stacks.", "ro": "Un cadavru între rafturi."},
	}
	questions := []contests.Question{{
		ID:        uuid.New(),
		ContestID: c.ID,
		Kind:      contests.KindFinal,
		Points:    10,
		IsVisible: true,
		Texts: map[string]contests.QuestionText{
			"en": {BodyMD: "Who did it?"},
			"ro": {BodyMD: "Cine a făcut-o?"},
		},
		Answers: []contests.Answer{{MatchKind: contests.MatchExactCI, Value: "the butler"}},
	}}
	return c, story, questions
}

func problemCodes(t *testing.T, err error) []string {
	t.Helper()

	var notReady *contests.NotPublishableError
	if !errors.As(err, &notReady) {
		t.Fatalf("contests.CheckPublishable() = %v, want a contests.NotPublishableError", err)
	}
	codes := make([]string, 0, len(notReady.Problems))
	for _, p := range notReady.Problems {
		codes = append(codes, p.Code)
	}
	return codes
}

func contains(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

func TestACompleteContestPassesTheGate(t *testing.T) {
	c, story, questions := publishable()

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

func TestGateRefusesAContestWithNoLanguages(t *testing.T) {
	c, story, questions := publishable()
	c.Languages = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemNoLanguages) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoLanguages)
	}
}

func TestGateNamesTheLanguageWhoseTitleIsMissing(t *testing.T) {
	// The organizer must be told which language to fix.
	c, story, questions := publishable()
	delete(c.Translations, "ro")

	var notReady *contests.NotPublishableError
	if !errors.As(contests.CheckPublishable(c, story, questions), &notReady) {
		t.Fatal("contests.CheckPublishable() = nil, want a missing translation")
	}
	for _, p := range notReady.Problems {
		if p.Code == contests.ProblemMissingContestTranslation && p.Lang == "ro" {
			return
		}
	}
	t.Errorf("problems = %+v, want a missing contest translation for ro", notReady.Problems)
}

func TestGateRefusesAContestWithNoStory(t *testing.T) {
	c, _, questions := publishable()

	codes := problemCodes(t, contests.CheckPublishable(c, contests.Story{}, questions))
	if !contains(codes, contests.ProblemNoStory) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoStory)
	}
}

func TestGateRefusesAStoryMissingALanguage(t *testing.T) {
	c, story, questions := publishable()
	delete(story.Bodies, "ro")

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemMissingStoryTranslation) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemMissingStoryTranslation)
	}
}

func TestGateRefusesAContestWithNoQuestions(t *testing.T) {
	c, story, _ := publishable()

	codes := problemCodes(t, contests.CheckPublishable(c, story, nil))
	if !contains(codes, contests.ProblemNoQuestions) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoQuestions)
	}
}

func TestSingleQuestionContestRefusesTwoQuestions(t *testing.T) {
	// The schema allows two while an organizer replaces one; only publication
	// insists.
	c, story, questions := publishable()
	c.QuestionMode = contests.QuestionModeSingle
	second := questions[0]
	second.ID = uuid.New()
	questions = append(questions, second)

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemSingleModeNeedsOneQuestion) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemSingleModeNeedsOneQuestion)
	}
}

func TestMultiQuestionContestAcceptsSeveralQuestions(t *testing.T) {
	c, story, questions := publishable()
	second := questions[0]
	second.ID = uuid.New()
	questions = append(questions, second)

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

func TestGateRefusesAQuestionWithNoReferenceAnswer(t *testing.T) {
	c, story, questions := publishable()
	questions[0].Answers = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemNoReferenceAnswer) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoReferenceAnswer)
	}
}

func TestHiddenQuestionStillNeedsItsTranslations(t *testing.T) {
	// An organizer may reveal it later.
	c, story, questions := publishable()
	questions[0].IsVisible = false
	delete(questions[0].Texts, "ro")

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemMissingQuestionTranslation) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemMissingQuestionTranslation)
	}
}

func TestGateRefusesAChoiceQuestionMissingALabel(t *testing.T) {
	c, story, questions := publishable()
	questions[0].Kind = contests.KindChoice
	questions[0].ChoiceIDs = []string{"a", "b"}
	questions[0].Answers = []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}}
	questions[0].Texts = map[string]contests.QuestionText{
		"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener"}},
		"ro": {BodyMD: "Cine a făcut-o?", Choices: map[string]string{"a": "Majordomul"}},
	}

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemMissingChoiceLabel) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemMissingChoiceLabel)
	}
}

func TestGateRefusesAFixedContestWithNoSchedule(t *testing.T) {
	c, story, questions := publishable()
	c.EndsAt = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemNoSchedule) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoSchedule)
	}
}

func TestGateReportsEveryProblemAtOnce(t *testing.T) {
	c, story, questions := publishable()
	c.Translations = nil
	story.Bodies = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if len(codes) < 4 {
		t.Errorf("problems = %v, want every missing translation reported", codes)
	}
}

// §6.1.1: an uncapped question closes only on a correct answer, so under
// sequential progression a stuck participant is trapped for the rest of the
// contest.
func TestGateRefusesSequentialWithAQuestionWithNoMaxAttempts(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	// publishable()'s one question already has MaxAttempts unset.

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemSequentialNeedsMaxAttempts) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemSequentialNeedsMaxAttempts)
	}
}

func TestGateAcceptsSequentialOnceEveryQuestionHasMaxAttempts(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	max := 3
	questions[0].MaxAttempts = &max

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

// §6.1.1: a hidden question never receives a submission, so it never closes
// and blocks everything after it, whatever its max_attempts.
func TestGateRefusesSequentialWithAHiddenQuestionBeforeAnother(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	max := 3
	questions[0].MaxAttempts = &max
	questions[0].Ord = 1
	questions[0].IsVisible = false
	second := questions[0]
	second.ID = uuid.New()
	second.Ord = 2
	second.IsVisible = true
	questions = append(questions, second)

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemSequentialHidesQuestion) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemSequentialHidesQuestion)
	}
}

func TestGateAcceptsSequentialWithAHiddenLastQuestion(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	max := 3
	questions[0].MaxAttempts = &max
	questions[0].Ord = 1
	second := questions[0]
	second.ID = uuid.New()
	second.Ord = 2
	second.IsVisible = false
	questions = append(questions, second)

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

func TestNotPublishableMatchesItsSentinel(t *testing.T) {
	c, story, _ := publishable()

	if err := contests.CheckPublishable(c, story, nil); !errors.Is(err, contests.ErrNotPublishable) {
		t.Errorf("contests.CheckPublishable() = %v, want it to match contests.ErrNotPublishable", err)
	}
}

// Winner mode names the winner by the final question (§6.1.1).
func TestGateRefusesWinnerScoringWithNoFinalQuestion(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringWinner
	questions[0].Kind = contests.KindText

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemWinnerNeedsFinal) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemWinnerNeedsFinal)
	}
}

func TestGateAcceptsWinnerScoringWithAFinalQuestion(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringWinner
	// publishable()'s one question is already a final one.
	limit := 3
	questions[0].MaxAttempts = &limit

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

// In winner mode a wrong final answer costs nothing, so an unlimited final
// question can be won by trying candidates.
func TestGateRefusesWinnerScoringWithAnUnlimitedFinalQuestion(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringWinner
	// publishable()'s one question is a final one with no attempt limit.

	var notReady *contests.NotPublishableError
	if !errors.As(contests.CheckPublishable(c, story, questions), &notReady) {
		t.Fatalf("contests.CheckPublishable() accepted an unlimited final question in winner mode")
	}
	var found bool
	for _, p := range notReady.Problems {
		if p.Code == contests.ProblemWinnerFinalNeedsAttemptLimit {
			found = true
			if p.QuestionID != questions[0].ID {
				t.Errorf("problem names question %s, want %s", p.QuestionID, questions[0].ID)
			}
		}
	}
	if !found {
		t.Errorf("problems = %+v, want %s", notReady.Problems, contests.ProblemWinnerFinalNeedsAttemptLimit)
	}
}

func TestGateAsksForAFinalAttemptLimitOnlyInWinnerMode(t *testing.T) {
	c, story, questions := publishable()
	limit := 3
	questions[0].MaxAttempts = &limit
	text := questions[0]
	text.ID = uuid.New()
	text.Kind = contests.KindText
	text.MaxAttempts = nil
	text.Texts = map[string]contests.QuestionText{"en": {BodyMD: "When?"}, "ro": {BodyMD: "Când?"}}

	c.Scoring = contests.ScoringWinner
	if err := contests.CheckPublishable(c, story, append(questions, text)); err != nil {
		t.Errorf("winner mode with an unlimited text question: CheckPublishable() = %v, want nil", err)
	}

	c.Scoring = contests.ScoringPoints
	questions[0].MaxAttempts = nil
	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("points mode with an unlimited final question: CheckPublishable() = %v, want nil", err)
	}
}

// A choice question's attempt limit is at most choices less correct ones, or
// a participant can exhaust every wrong option (docs/ARCHITECTURE.md §6.1.1).

// asChoiceQuestion labels every choice in every language so attempt-limit
// tests do not also trip ProblemMissingChoiceLabel.
func asChoiceQuestion(q contests.Question) contests.Question {
	return withChoices(q, []string{"a", "b", "c"}, "a")
}

func withChoices(q contests.Question, ids []string, correct ...string) contests.Question {
	q.Kind = contests.KindChoice
	q.ChoiceIDs = ids
	for lang, text := range q.Texts {
		text.Choices = make(map[string]string, len(ids))
		for _, id := range ids {
			text.Choices[id] = "Option " + id
		}
		q.Texts[lang] = text
	}
	q.Answers = nil
	for _, id := range correct {
		q.Answers = append(q.Answers, contests.Answer{MatchKind: contests.MatchExact, Value: id})
	}
	return q
}

// n options with k correct are always solved by attempt n-k+1, so the limit
// is held to n-k.
func TestGateHoldsAnICPCChoiceLimitBelowTheWrongChoicesPlusOne(t *testing.T) {
	cases := []struct {
		name    string
		correct []string
		limit   int
		refused bool
	}{
		{"one correct of four, limit 3", []string{"a"}, 3, false},
		{"one correct of four, limit 4", []string{"a"}, 4, true},
		{"two correct of four, limit 2", []string{"a", "c"}, 2, false},
		{"two correct of four, limit 3", []string{"a", "c"}, 3, true},
		// The same correct choice written twice is still one choice.
		{"one correct of four written twice, limit 3", []string{"a", "a"}, 3, false},
		{"every choice correct, limit 1", []string{"a", "b", "c", "d"}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, story, questions := publishable()
			c.Scoring = contests.ScoringICPC
			questions[0] = withChoices(questions[0], []string{"a", "b", "c", "d"}, tc.correct...)
			limit := tc.limit
			questions[0].MaxAttempts = &limit

			err := contests.CheckPublishable(c, story, questions)
			if tc.refused {
				codes := problemCodes(t, err)
				if !contains(codes, contests.ProblemICPCChoiceNeedsAttemptLimit) {
					t.Errorf("problems = %v, want %s", codes, contests.ProblemICPCChoiceNeedsAttemptLimit)
				}
				return
			}
			if err != nil {
				t.Errorf("contests.CheckPublishable() = %v, want nil", err)
			}
		})
	}
}

func TestGateRefusesAnICPCChoiceQuestionWithNoAttemptLimit(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringICPC
	questions[0] = asChoiceQuestion(questions[0])
	questions[0].MaxAttempts = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemICPCChoiceNeedsAttemptLimit) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemICPCChoiceNeedsAttemptLimit)
	}
}

func TestGateRefusesAnICPCChoiceQuestionWithALimitEqualToTheChoiceCount(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringICPC
	questions[0] = asChoiceQuestion(questions[0])
	limit := 3
	questions[0].MaxAttempts = &limit

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemICPCChoiceNeedsAttemptLimit) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemICPCChoiceNeedsAttemptLimit)
	}
}

func TestGateAcceptsAnICPCChoiceQuestionWithALimitBelowTheChoiceCount(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringICPC
	questions[0] = asChoiceQuestion(questions[0])
	limit := 2
	questions[0].MaxAttempts = &limit

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

// Outside ICPC brute force costs nothing at all, so it is refused too, with
// its own problem code.
func TestGateRefusesAnUncappedChoiceQuestionOutsideICPCMode(t *testing.T) {
	c, story, questions := publishable()
	questions[0] = asChoiceQuestion(questions[0])
	questions[0].MaxAttempts = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemChoiceNeedsAttemptLimit) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemChoiceNeedsAttemptLimit)
	}
	if contains(codes, contests.ProblemICPCChoiceNeedsAttemptLimit) {
		t.Errorf("problems = %v, want the points-mode wording, not ICPC's", codes)
	}
}

// The window can move after the freeze was saved.
func TestGateRefusesAFreezeThatNoLongerFitsTheWindow(t *testing.T) {
	c, story, questions := publishable()
	freeze := 180 // publishable()'s window is exactly three hours
	c.LeaderboardFreezeMin = &freeze

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemLeaderboardFreezeExceedsWindow) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemLeaderboardFreezeExceedsWindow)
	}
}

// A freeze is measured back from ends_at.
func TestGateRefusesAFreezeWithNoEndToMeasureFrom(t *testing.T) {
	c, story, questions := publishable()
	duration := 60
	c.Timing, c.DurationMin, c.EndsAt = contests.TimingIndividual, &duration, nil
	freeze := 30
	c.LeaderboardFreezeMin = &freeze

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemLeaderboardFreezeExceedsWindow) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemLeaderboardFreezeExceedsWindow)
	}
}

// Without ends_at it never finishes: the leaderboard never finalises and its
// game databases are never reclaimed.
func TestGateRefusesAnIndividualContestWithNoEnd(t *testing.T) {
	c, story, questions := publishable()
	minutes := 120
	c.Timing = contests.TimingIndividual
	c.DurationMin = &minutes
	c.EndsAt = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemNoSchedule) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoSchedule)
	}
}

func TestGateRefusesAnUncappedChoiceQuestionInPointsScoring(t *testing.T) {
	c, story, questions := publishable()
	c.Scoring = contests.ScoringPoints
	questions[0].Kind = contests.KindChoice
	questions[0].ChoiceIDs = []string{"a", "b", "c"}
	questions[0].MaxAttempts = nil
	questions[0].Answers = []contests.Answer{{MatchKind: contests.MatchExact, Value: "a"}}
	questions[0].Texts = map[string]contests.QuestionText{
		"en": {BodyMD: "Who did it?", Choices: map[string]string{"a": "The butler", "b": "The gardener", "c": "The cook"}},
		"ro": {BodyMD: "Cine a făcut-o?", Choices: map[string]string{"a": "Majordomul", "b": "Grădinarul", "c": "Bucătarul"}},
	}

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemChoiceNeedsAttemptLimit) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemChoiceNeedsAttemptLimit)
	}
}
