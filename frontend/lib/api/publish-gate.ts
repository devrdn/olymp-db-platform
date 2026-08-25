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

export type LanguageRow = {
  lang: string;
  title: "ok" | "missing";
  story: "ok" | "missing";
  questionsMissing: number;
};

/** A problem that belongs to the contest, not to one of its languages. */
export type GlobalProblem = { code: string; detail?: string };

export type PublishGate = { global: GlobalProblem[]; byLanguage: LanguageRow[] };

export function summarisePublishCheck(check: PublishCheck, languages: string[]): PublishGate {
  const byLanguage = languages.map((lang) => {
    const named = check.problems.filter((p) => p.lang === lang);
    return {
      lang,
      title: named.some((p) => p.code === PUBLISH_PROBLEMS.missingContestTranslation)
        ? ("missing" as const)
        : ("ok" as const),
      story: named.some((p) => p.code === PUBLISH_PROBLEMS.missingStoryTranslation)
        ? ("missing" as const)
        : ("ok" as const),
      questionsMissing: named.filter(
        (p) => p.code === PUBLISH_PROBLEMS.missingQuestionTranslation,
      ).length,
    };
  });

  // A problem without a language is about the contest itself: no schedule, no
  // questions, the wrong number of them for the mode. It has no cell to sit in.
  const global = check.problems
    .filter((p) => !p.lang)
    .map((p) => ({ code: p.code, detail: p.detail }));

  return { global, byLanguage };
}
