/**
 * The vocabulary a question is written in, apart from the schemas that validate the wire.
 *
 * A schema module calls `z.object()` when it loads, so a bundler cannot drop
 * it — and a client component importing one string array from such a module
 * ships the whole of zod with it: 280 KB of parser for a row of buttons. The
 * schemas import these, so a term still has one definition.
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
