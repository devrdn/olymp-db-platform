/**
 * Turns the publish check into the matrix the constructor renders.
 *
 * `GET /publish-check` answers 200 even when publishing is impossible: being
 * asked what is left is not a failure. The response is a flat list of problems,
 * each optionally naming a language and a question. The constructor shows a
 * row per declared language, so the flat list is regrouped here rather than in
 * the component.
 */

export const PUBLISH_PROBLEMS = {
  noLanguages: "no_languages",
  missingContestTranslation: "missing_contest_translation",
  noStory: "no_story",
  missingStoryTranslation: "missing_story_translation",
  noQuestions: "no_questions",
  singleModeNeedsOneQuestion: "single_mode_needs_one_question",
  missingQuestionTranslation: "missing_question_translation",
  missingChoiceLabel: "missing_choice_label",
  noReferenceAnswer: "no_reference_answer",
  noSchedule: "no_schedule",
  // Sequential-progression-only refusals (backend/internal/contests/publish.go):
  // a question with no attempt cap, or a hidden one with another ordered
  // after it, either of which could trap a participant on the day it costs
  // most. Neither has a cell of its own — both name a question, not a
  // language — so they fall into the same global list every other
  // contest-wide problem does.
  sequentialNeedsMaxAttempts: "sequential_needs_max_attempts",
  sequentialHidesQuestion: "sequential_hides_question",
  // Contest-wide, like the two above: winner mode with no final question, and
  // a leaderboard freeze that no longer fits the window.
  winnerNeedsFinal: "winner_needs_final",
  leaderboardFreezeExceedsWindow: "leaderboard_freeze_exceeds_window",
} as const;

export type PublishProblem = {
  code: string;
  lang?: string;
  question_id?: string;
  detail?: string;
};

export type PublishCheck = { ready: boolean; problems: PublishProblem[] };

/**
 * A cell in the language matrix.
 *
 * Three states, not two. "There is no story in Romanian" and "there is no
 * story at all" are different facts, and collapsing the second into "done"
 * — which is what happens when only per-language problems are consulted —
 * prints a tick beside a language whose story does not exist. The author is
 * then told, in the same panel, that the story is missing and that every
 * language has one.
 */
export type CellState = "ok" | "missing" | "not-started";

export type LanguageRow = {
  lang: string;
  title: CellState;
  story: CellState;
  questionsMissing: number;
  /** No questions exist yet, so "none untranslated" is not an achievement. */
  questionsNotStarted: boolean;
};

/**
 * A problem that belongs to the contest, not to one of its languages.
 *
 * `count` because several of these arrive under one code: the gate reports a
 * missing reference answer once per question. Listed as they come, two
 * questions produce two identical sentences — neither naming which question,
 * so the repetition tells an author nothing — and two list children under one
 * key, which React will not guarantee the rendering of.
 */
export type GlobalProblem = { code: string; count: number; detail?: string };

export type PublishGate = { global: GlobalProblem[]; byLanguage: LanguageRow[] };

export function summarisePublishCheck(check: PublishCheck, languages: string[]): PublishGate {
  // A problem without a language is about the contest itself: no schedule, no
  // questions, the wrong number of them for the mode. It has no cell to sit in.
  //
  // Grouped by code, in the order the codes first appear. The detail survives
  // only on a code that occurs once: where several arrive, each one describes a
  // different question, and showing one of them beside a count would say
  // something true of a single question as though it were true of all of them.
  const global: GlobalProblem[] = [];
  for (const problem of check.problems) {
    if (problem.lang) continue;

    const seen = global.find((entry) => entry.code === problem.code);
    if (!seen) {
      global.push({ code: problem.code, count: 1, ...(problem.detail ? { detail: problem.detail } : {}) });
      continue;
    }
    seen.count += 1;
    delete seen.detail;
  }

  // What does not exist yet cannot be translated. The gate reports "no story"
  // once, without a language, and says nothing further about any language's
  // story — so a matrix built only from per-language problems concludes that
  // every language's story is in order.
  const noStory = global.some((p) => p.code === PUBLISH_PROBLEMS.noStory);
  const noQuestions = global.some((p) => p.code === PUBLISH_PROBLEMS.noQuestions);

  const byLanguage = languages.map((lang) => {
    const named = check.problems.filter((p) => p.lang === lang);
    const missing = (code: string): CellState =>
      named.some((p) => p.code === code) ? "missing" : "ok";

    return {
      lang,
      title: missing(PUBLISH_PROBLEMS.missingContestTranslation),
      story: noStory ? ("not-started" as const) : missing(PUBLISH_PROBLEMS.missingStoryTranslation),
      // A question with no text and a question whose choice has no label are
      // both a question that is not finished in this language. Counting only
      // the first left the second reported nowhere at all: it names a language,
      // so it is not a contest-wide problem, and nothing in the matrix looked
      // for it — the panel called every language complete while the gate went
      // on refusing to publish.
      questionsMissing: named.filter(
        (p) =>
          p.code === PUBLISH_PROBLEMS.missingQuestionTranslation ||
          p.code === PUBLISH_PROBLEMS.missingChoiceLabel,
      ).length,
      questionsNotStarted: noQuestions,
    };
  });

  return { global, byLanguage };
}
