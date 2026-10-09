/**
 * The vocabulary a question is written in, apart from the wire schemas.
 *
 * Every `*-terms` module exists for the same reason: a schema module calls
 * `z.object()` at load, so a bundler cannot drop it, and a client component
 * importing one array from it would ship all of zod (about 280 KB). The
 * schemas import these, so each term keeps one definition.
 */

import type { Question } from "./content";

export const QUESTION_KINDS = ["text", "choice", "final"] as const;

export const MATCH_KINDS = ["exact", "exact_ci", "regex"] as const;

export function answerable(question: Question): boolean {
  return question.answers.length > 0;
}

export function untranslated(question: Question, languages: string[]): string[] {
  return languages.filter((lang) => !question.texts[lang]?.bodyMd);
}
