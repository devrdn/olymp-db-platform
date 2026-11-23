import type { Dictionary } from "./dictionary";
import { labelFor } from "./label";

/** The errors section, or a part of it that keeps the fallback, as screens with a scoped dictionary hold it. */
export type ErrorSentences = Pick<Dictionary["errors"], "fallback">;

/**
 * The error text for an API code, from the active dictionary. The server's
 * English `message` never reaches the interface. An unknown code (newer than
 * this build) gets the fallback sentence. Takes `dict.errors`, or a part of it
 * that keeps the fallback, so screens with a scoped dictionary can call it.
 */
export function messageForCode(code: string, errors: ErrorSentences): string {
  return labelFor(errors, code) ?? errors.fallback;
}
