package contests_test

import (
	"errors"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/google/uuid"
)

// publishable returns a contest that satisfies the gate, so each test states
// exactly the one thing it takes away.
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

// problemCodes lists the codes reported, so a test can assert on the reason
// rather than merely on failure.
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
	// A participant who picked Romanian must not reach an empty page, and the
	// organizer has to be told which language to fix.
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
	// The invariant the schema deliberately does not enforce: an organizer
	// passes through two questions while replacing one, and only publication
	// has to insist.
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
	// Ungradable, and nobody finds out until the contest is scored.
	c, story, questions := publishable()
	questions[0].Answers = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemNoReferenceAnswer) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemNoReferenceAnswer)
	}
}

func TestHiddenQuestionStillNeedsItsTranslations(t *testing.T) {
	// An organizer may reveal it later, and the payload must not depend on
	// whether somebody happened to fill the text in.
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
	// An organizer fixing a contest one refusal at a time would need as many
	// round trips as they have missing translations.
	c, story, questions := publishable()
	c.Translations = nil
	story.Bodies = nil

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if len(codes) < 4 {
		t.Errorf("problems = %v, want every missing translation reported", codes)
	}
}

// §6.1.1: sequential progression opens the next question only when the
// previous one is closed, and a question with no attempt cap can only ever
// close by a correct answer — trapping a participant who is stuck for the
// rest of the contest. The gate refuses this at publish, the one moment it
// can still be caught rather than discovered live.
func TestGateRefusesSequentialWithAQuestionWithNoMaxAttempts(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	// publishable()'s one question already has MaxAttempts unset.

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemSequentialNeedsMaxAttempts) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemSequentialNeedsMaxAttempts)
	}
}

// The same contest passes once every question carries an attempt cap: the
// gate is about the trap, not about progression itself.
func TestGateAcceptsSequentialOnceEveryQuestionHasMaxAttempts(t *testing.T) {
	c, story, questions := publishable()
	c.Progression = contests.ProgressionSequential
	max := 3
	questions[0].MaxAttempts = &max

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

// §6.1.1: a hidden question can never receive a submission — a participant is
// never given its identifier — so it can never close, and everything ordered
// after it in a sequential contest becomes unreachable for the rest of the
// contest. That holds even with a perfectly good max_attempts on the hidden
// question itself, which is the one shape ProblemSequentialNeedsMaxAttempts
// does not catch.
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

// A hidden question with nothing ordered after it blocks nothing, and works
// exactly as authored — the same as it would in free progression.
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

// Winner mode names the winner by the final question (§6.1.1). Without one the
// contest can end with nobody placed at all, which is exactly the day nobody
// can fix it.
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

	if err := contests.CheckPublishable(c, story, questions); err != nil {
		t.Errorf("contests.CheckPublishable() = %v, want nil", err)
	}
}

// Saving already refuses a freeze as long as the window, but the window can
// move after the freeze was saved; the gate is the last moment that is cheap.
func TestGateRefusesAFreezeThatNoLongerFitsTheWindow(t *testing.T) {
	c, story, questions := publishable()
	freeze := 180 // publishable()'s window is exactly three hours
	c.LeaderboardFreezeMin = &freeze

	codes := problemCodes(t, contests.CheckPublishable(c, story, questions))
	if !contains(codes, contests.ProblemLeaderboardFreezeExceedsWindow) {
		t.Errorf("problems = %v, want %s", codes, contests.ProblemLeaderboardFreezeExceedsWindow)
	}
}

// A freeze is measured back from ends_at. An individual-timing contest may
// publish with no ends_at, and then there is nothing to measure from.
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
