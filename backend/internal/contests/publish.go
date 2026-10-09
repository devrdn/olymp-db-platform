package contests

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Publish problem codes, translated by the interface (§6.2).
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
	// ProblemSequentialNeedsMaxAttempts names an uncapped question in a
	// sequential contest (§6.1.1): a participant stuck on it could never
	// reach the next one.
	ProblemSequentialNeedsMaxAttempts = "sequential_needs_max_attempts"
	// ProblemSequentialHidesQuestion names a hidden question with another
	// ordered after it in a sequential contest (§6.1.1). A participant never
	// gets a hidden question's identifier, so it can never be answered or
	// spent, and everything after it is unreachable.
	ProblemSequentialHidesQuestion = "sequential_hides_question"
	// ProblemWinnerNeedsFinal names a winner-mode contest with no final
	// question, which would end with nobody placed (§6.1.1).
	ProblemWinnerNeedsFinal = "winner_needs_final"
	// ProblemWinnerFinalNeedsAttemptLimit names an uncapped final question in
	// winner mode, where a wrong answer costs nothing and the contest could
	// be won by trying candidates.
	ProblemWinnerFinalNeedsAttemptLimit = "winner_final_needs_attempt_limit"
	// ProblemLeaderboardFreezeExceedsWindow names a freeze that begins before
	// the window opens, or one with no ends_at to be measured back from.
	// Saving refuses the first already, but the window can move afterwards.
	ProblemLeaderboardFreezeExceedsWindow = "leaderboard_freeze_exceeds_window"
	// ProblemICPCChoiceNeedsAttemptLimit names a choice question in ICPC
	// scoring whose attempt cap is missing or above its choices less its
	// correct choices, so it can be solved by trying options for penalty time.
	ProblemICPCChoiceNeedsAttemptLimit = "icpc_choice_needs_attempt_limit"
	// ProblemChoiceNeedsAttemptLimit is the same fault outside ICPC, where
	// trying options wins the question's points.
	ProblemChoiceNeedsAttemptLimit = "choice_needs_attempt_limit"
	// ProblemStaffRegistered names a registered participant whose account
	// holds contest.admin_all (see ErrStaffCannotParticipate). Registration
	// refuses one, but not a registration made before the grant. Detail is
	// the login, so the organizer can remove that person.
	ProblemStaffRegistered = "staff_registered"
	// ProblemCoverNeedsAttribution names an uploaded cover with no credit
	// line (§10.1). The upload route and the column check it too; this
	// catches a restored or hand-repaired row, or a whitespace-only credit.
	ProblemCoverNeedsAttribution = "cover_needs_attribution"
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

// NotPublishableError collects every reason at once, so an organizer fixes
// them in one pass.
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

// CheckPublishable reports what in a contest's own content stands between it
// and its participants. The schema does not enforce these, since a draft
// passes through all of them (§6.1).
//
// Content only: the roster and the cover are checked by checkPublishable,
// which a caller holding repositories should use instead.
func CheckPublishable(c Contest, story Story, questions []Question) error {
	if problems := publishProblems(c, story, questions); len(problems) > 0 {
		return &NotPublishableError{Problems: problems}
	}
	return nil
}

func publishProblems(c Contest, story Story, questions []Question) []PublishProblem {
	var problems []PublishProblem
	add := func(p PublishProblem) { problems = append(problems, p) }

	langs := c.LanguageCodes()
	if len(langs) == 0 {
		add(PublishProblem{Code: ProblemNoLanguages})
	}

	// Individual timing needs ends_at too: nothing else ever finishes the
	// contest, so its leaderboard would never finalise and its game
	// databases never be reclaimed.
	if c.StartsAt == nil || c.EndsAt == nil {
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

	// questions need not arrive sorted by Ord.
	maxOrd := 0
	for _, q := range questions {
		if q.Ord > maxOrd {
			maxOrd = q.Ord
		}
	}

	for _, q := range questions {
		checkQuestionPublishable(q, langs, add)
		// With n options of which k are correct, attempt n-k+1 is sure to
		// hit one, so the cap must be at most n-k. The rate limit is no
		// obstacle to a handful of options.
		if q.Kind == KindChoice && !choiceCapped(q) {
			code := ProblemChoiceNeedsAttemptLimit
			if c.Scoring == ScoringICPC {
				code = ProblemICPCChoiceNeedsAttemptLimit
			}
			add(PublishProblem{Code: code, QuestionID: q.ID})
		}
		if c.Scoring == ScoringWinner && q.Kind == KindFinal && q.MaxAttempts == nil {
			add(PublishProblem{Code: ProblemWinnerFinalNeedsAttemptLimit, QuestionID: q.ID})
		}
		if c.Progression == ProgressionSequential {
			if q.MaxAttempts == nil {
				add(PublishProblem{Code: ProblemSequentialNeedsMaxAttempts, QuestionID: q.ID})
			}
			if !q.IsVisible && q.Ord < maxOrd {
				add(PublishProblem{Code: ProblemSequentialHidesQuestion, QuestionID: q.ID})
			}
		}
	}

	if c.Scoring == ScoringWinner && !slices.ContainsFunc(questions, func(q Question) bool {
		return q.Kind == KindFinal
	}) {
		add(PublishProblem{Code: ProblemWinnerNeedsFinal})
	}
	if !c.FreezeFitsWindow() {
		add(PublishProblem{Code: ProblemLeaderboardFreezeExceedsWindow})
	}

	return problems
}

// choiceCapped reports whether a choice question's attempt limit keeps it
// from being solved by trying options. A question with no correct option is
// treated as having one, so a broken answer key does not loosen the bound.
// With every option correct, no cap is low enough.
func choiceCapped(q Question) bool {
	if q.MaxAttempts == nil {
		return false
	}
	n, k := len(q.ChoiceIDs), max(q.CorrectChoices(), 1)
	if k >= n {
		return false
	}
	return *q.MaxAttempts <= n-k
}

// checkQuestionPublishable collects one question's reasons.
func checkQuestionPublishable(q Question, langs []string, add func(PublishProblem)) {
	if len(q.Answers) == 0 {
		add(PublishProblem{Code: ProblemNoReferenceAnswer, QuestionID: q.ID})
	}

	for _, lang := range langs {
		text, ok := q.Texts[lang]
		// Hidden questions too: an organizer may reveal one later.
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
