package contests

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Publish problem codes.
//
// Machine codes rather than sentences: the API already answers in codes and
// the interface translates them (§6.2), and an admin screen wants to point at
// the offending language or question, not print a paragraph.
const (
	ProblemNoLanguages                = "no_languages"
	ProblemMissingContestTranslation  = "missing_contest_translation"
	ProblemNoStory                    = "no_story"
	ProblemMissingStoryTranslation    = "missing_story_translation"
	ProblemNoQuestions                = "no_questions"
	ProblemSingleModeNeedsOneQuestion = "single_mode_needs_one_question"
	ProblemMissingQuestionTranslation = "missing_question_translation"
	ProblemMissingChoiceLabel         = "missing_choice_label"
	ProblemNoReferenceAnswer          = "no_reference_answer"
	ProblemNoSchedule                 = "no_schedule"
)

// PublishProblem is one reason a contest is not ready.
type PublishProblem struct {
	Code string
	// Lang names the language at fault, when one is.
	Lang string
	// QuestionID names the question at fault, when one is.
	QuestionID uuid.UUID
	// Detail carries what neither of the above can express.
	Detail string
}

// ErrNotPublishable classifies every refusal of the gate, so a caller can
// recognise one with errors.Is without unwrapping the detail.
var ErrNotPublishable = errors.New("contest is not ready to publish")

// NotPublishableError collects every reason at once.
//
// All of them, not the first: an organizer fixing a contest one refusal at a
// time would need as many round trips as they have missing translations.
type NotPublishableError struct {
	Problems []PublishProblem
}

func (e *NotPublishableError) Error() string {
	codes := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		codes = append(codes, p.Code)
	}
	return "contest is not ready to publish: " + strings.Join(codes, ", ")
}

// Is makes errors.Is(err, ErrNotPublishable) true for the detailed error.
func (e *NotPublishableError) Is(target error) bool { return target == ErrNotPublishable }

// CheckPublishable reports everything standing between a contest and its
// participants.
//
// This is the gate the schema deliberately does not enforce (see §6.1): a
// contest under construction passes through every one of these states, and a
// constraint would fight the editor. The invariants only have to hold at the
// moment of publication, which is here.
func CheckPublishable(c Contest, story Story, questions []Question) error {
	var problems []PublishProblem
	add := func(p PublishProblem) { problems = append(problems, p) }

	langs := c.LanguageCodes()
	if len(langs) == 0 {
		add(PublishProblem{Code: ProblemNoLanguages})
	}

	// A fixed-window contest with no window has nothing to open it and nothing
	// to close it; an individual one at least needs a moment to open.
	if c.StartsAt == nil || (c.Timing == TimingFixed && c.EndsAt == nil) {
		add(PublishProblem{Code: ProblemNoSchedule})
	}

	for _, lang := range langs {
		if strings.TrimSpace(c.Translations[lang].Title) == "" {
			add(PublishProblem{Code: ProblemMissingContestTranslation, Lang: lang})
		}
	}

	if story.ID == uuid.Nil {
		add(PublishProblem{Code: ProblemNoStory})
	} else {
		for _, lang := range langs {
			if strings.TrimSpace(story.Bodies[lang]) == "" {
				add(PublishProblem{Code: ProblemMissingStoryTranslation, Lang: lang})
			}
		}
	}

	switch {
	case len(questions) == 0:
		add(PublishProblem{Code: ProblemNoQuestions})
	case c.QuestionMode == QuestionModeSingle && len(questions) != 1:
		add(PublishProblem{
			Code:   ProblemSingleModeNeedsOneQuestion,
			Detail: fmt.Sprintf("%d questions", len(questions)),
		})
	}

	for _, q := range questions {
		checkQuestionPublishable(q, langs, add)
	}

	if len(problems) > 0 {
		return &NotPublishableError{Problems: problems}
	}
	return nil
}

// checkQuestionPublishable collects one question's reasons.
func checkQuestionPublishable(q Question, langs []string, add func(PublishProblem)) {
	if len(q.Answers) == 0 {
		add(PublishProblem{Code: ProblemNoReferenceAnswer, QuestionID: q.ID})
	}

	for _, lang := range langs {
		text, ok := q.Texts[lang]
		// A hidden question is authored in every language too: an organizer
		// may reveal it later, and the participant-facing payload must not
		// depend on whether somebody happened to fill it in.
		if !ok || strings.TrimSpace(text.BodyMD) == "" {
			add(PublishProblem{Code: ProblemMissingQuestionTranslation, Lang: lang, QuestionID: q.ID})
			continue
		}

		for _, choice := range q.ChoiceIDs {
			if strings.TrimSpace(text.Choices[choice]) == "" {
				add(PublishProblem{
					Code:       ProblemMissingChoiceLabel,
					Lang:       lang,
					QuestionID: q.ID,
					Detail:     choice,
				})
			}
		}
	}
}
