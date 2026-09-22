package contests

import (
	"errors"
	"fmt"
	"slices"
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
	// ProblemSequentialNeedsMaxAttempts names a question with no attempt
	// limit in a sequential contest (§6.1.1): opening only on a correct
	// answer would trap a participant stuck on it for the rest of the
	// contest, clock still running, and this is the one moment that trap can
	// still be caught rather than discovered live.
	ProblemSequentialNeedsMaxAttempts = "sequential_needs_max_attempts"
	// ProblemSequentialHidesQuestion names a hidden question (is_visible =
	// false) that has another question ordered after it in a sequential
	// contest (§6.1.1). Sequential progression closes a question by a
	// correct answer or by spending every attempt, and both require a
	// submission — but a participant is never given a hidden question's
	// identifier (VisibleQuestionRepository.ForContest never selects one), so
	// it can never receive a submission and never close. Every question
	// ordered after it is then unreachable for the rest of the contest: the
	// same trap ProblemSequentialNeedsMaxAttempts exists to catch, in the one
	// shape that check misses, since a hidden question can carry a perfectly
	// good max_attempts and still never be spent. Hidden questions work
	// exactly as authored in free progression and in single-question mode
	// (§6.1) — sequential is the one progression where invisibility itself
	// becomes the lockout, which is why the refusal lives here rather than
	// forbidding is_visible = false outright.
	ProblemSequentialHidesQuestion = "sequential_hides_question"
	// ProblemWinnerNeedsFinal names a winner-mode contest with no final
	// question (§6.1.1): the winner is whoever first answers the final
	// question, so without one the contest ends with nobody placed.
	ProblemWinnerNeedsFinal = "winner_needs_final"
	// ProblemWinnerFinalNeedsAttemptLimit names a final question with no
	// attempt limit in a winner-mode contest. There the first correct final
	// answer wins outright and a wrong one costs nothing (penaltyAmount is
	// zero in this mode), so without a limit the contest can be won by
	// trying candidates one after another rather than by solving it — the
	// answer rate slows that down, only a limit stops it.
	ProblemWinnerFinalNeedsAttemptLimit = "winner_final_needs_attempt_limit"
	// ProblemLeaderboardFreezeExceedsWindow names a freeze that begins before
	// the window opens, or one with no ends_at to be measured back from.
	// Saving refuses the first already, but the window can move afterwards.
	ProblemLeaderboardFreezeExceedsWindow = "leaderboard_freeze_exceeds_window"
	// ProblemICPCChoiceNeedsAttemptLimit names a choice question, in ICPC
	// scoring, with no attempt cap or one above its number of choices less
	// its number of correct choices — decision 3 of the design doc's own
	// decisions section: without one, a participant can submit option after
	// option and solve the question for the mere cost of penalty time, never
	// actually needing to know the answer.
	ProblemICPCChoiceNeedsAttemptLimit = "icpc_choice_needs_attempt_limit"
	// ProblemChoiceNeedsAttemptLimit names the same fault outside ICPC
	// scoring, where it costs even less: the options are listed in the
	// participant's own page, so an uncapped choice question is answered by
	// sending them one after another — and there the prize is the question's
	// points rather than penalty time.
	ProblemChoiceNeedsAttemptLimit = "choice_needs_attempt_limit"
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

	// A contest with no window has nothing to open it and nothing to close
	// it. An individual one needs an end as much as a fixed one does, for a
	// reason its own participants never see: nothing else ever moves it out
	// of "running", so the leaderboard never freezes or finalises and the
	// game databases behind it are never reclaimed. Each participant's own
	// timer ends their work; ends_at is what ends the contest.
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

	// The highest display position among this contest's questions — computed
	// once, rather than assuming questions arrives sorted by Ord, since
	// CheckPublishable is exported and this is the only thing below that
	// needs "is anything ordered after this one" rather than a per-question
	// fact.
	maxOrd := 0
	for _, q := range questions {
		if q.Ord > maxOrd {
			maxOrd = q.Ord
		}
	}

	for _, q := range questions {
		checkQuestionPublishable(q, langs, add)
		// Decision 3 of the design doc: a choice question must not be
		// solvable by trying options. With n options of which k are correct,
		// attempt n-k+1 is sure to hit a correct one, so the cap must be at
		// most n-k. The rule was written for ICPC, where the brute force
		// costs penalty time; it holds in every mode, because the options
		// are in the participant's own page either way and the rate limit
		// (six answers a minute) is no obstacle to five of them. Two codes
		// because the two say different things to an organiser: in ICPC the
		// cost is penalty time, elsewhere it is the question's points.
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
			// §6.1.1: sequential progression opens the next question only
			// once the previous one is closed — answered correctly, or every
			// attempt spent. A question with no attempt cap can only ever
			// close the first way, so a participant stuck on it never
			// reaches anything after it; refusing this at publish is
			// refusing the one shape of contest that can trap a participant
			// on the day it costs most.
			if q.MaxAttempts == nil {
				add(PublishProblem{Code: ProblemSequentialNeedsMaxAttempts, QuestionID: q.ID})
			}
			// A hidden question can never receive a submission (a
			// participant is never given its identifier), so it can never
			// close — not by a correct answer, and not by exhausting
			// max_attempts either, however that field is set. Everything
			// ordered after it is then unreachable for the rest of the
			// contest: the same lockout as above, in the one shape that
			// check does not see. A hidden question with nothing after it is
			// unaffected — it works exactly as authored, the way it does in
			// free progression.
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

	if len(problems) > 0 {
		return &NotPublishableError{Problems: problems}
	}
	return nil
}

// choiceCapped reports whether a choice question's attempt limit keeps it
// from being solved by trying options.
//
// A question none of whose options is correct is counted as having one: it
// cannot be brute-forced at all, and holding it to the stricter single-answer
// bound keeps the rule from loosening for an answer that is broken anyway
// (ProblemNoReferenceAnswer covers the question with no answer at all). Every
// option correct leaves no bound to meet: any single attempt solves it.
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
