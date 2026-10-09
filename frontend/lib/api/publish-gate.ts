/**
 * Turns the publish check's flat problem list (200 even when publishing is
 * impossible) into the per-language matrix the constructor renders.
 */

/** The problem codes the publish check reports. */
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
  // The codes below name no language, so they land in the global list.
  // Sequential progression: an uncapped question, or a hidden one with another
  // after it, could trap a participant.
  sequentialNeedsMaxAttempts: "sequential_needs_max_attempts",
  sequentialHidesQuestion: "sequential_hides_question",
  winnerNeedsFinal: "winner_needs_final",
  // A final question with no attempt limit is won by guessing.
  winnerFinalNeedsAttemptLimit: "winner_final_needs_attempt_limit",
  leaderboardFreezeExceedsWindow: "leaderboard_freeze_exceeds_window",
  // A choice question whose attempt limit is not below its option count can be
  // guessed through; two codes because the cost differs (penalty time under
  // ICPC, points otherwise).
  icpcChoiceNeedsAttemptLimit: "icpc_choice_needs_attempt_limit",
  choiceNeedsAttemptLimit: "choice_needs_attempt_limit",
  // A roster member who administers every contest and so can read the
  // answers. The detail is their login.
  staffRegistered: "staff_registered",
  // An uploaded cover with nobody credited; a drawn cover never appears here.
  coverNeedsAttribution: "cover_needs_attribution",
} as const;

export type PublishProblem = {
  code: string;
  lang?: string;
  question_id?: string;
  detail?: string;
};

export type PublishCheck = { ready: boolean; problems: PublishProblem[] };

/**
 * A cell in the language matrix. "not-started" keeps "no story at all" from
 * showing as a tick beside every language.
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
 * A problem of the contest, not a language. Problems sharing a code (one per
 * question) collapse into a `count`, avoiding identical sentences and
 * duplicate React keys.
 */
export type GlobalProblem = { code: string; count: number; detail?: string };

export type PublishGate = { global: GlobalProblem[]; byLanguage: LanguageRow[] };

export function summarisePublishCheck(check: PublishCheck, languages: string[]): PublishGate {
  // Grouped by code in first-seen order. The detail survives only on a code
  // that occurs once: with several, it would describe one question as if all.
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

  // "No story" arrives once, without a language, and no per-language problem
  // follows it.
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
      // An unlabelled choice also leaves the question unfinished in this language.
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
