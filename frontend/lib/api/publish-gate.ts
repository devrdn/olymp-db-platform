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

/** A problem that belongs to the contest, not to one of its languages. */
export type GlobalProblem = { code: string; detail?: string };

export type PublishGate = { global: GlobalProblem[]; byLanguage: LanguageRow[] };

export function summarisePublishCheck(check: PublishCheck, languages: string[]): PublishGate {
  // A problem without a language is about the contest itself: no schedule, no
  // questions, the wrong number of them for the mode. It has no cell to sit in.
  const global = check.problems
    .filter((p) => !p.lang)
    .map((p) => ({ code: p.code, detail: p.detail }));

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
      questionsMissing: named.filter(
        (p) => p.code === PUBLISH_PROBLEMS.missingQuestionTranslation,
      ).length,
      questionsNotStarted: noQuestions,
    };
  });

  return { global, byLanguage };
}
