import type { Dictionary } from "./dictionary";

type Errors = Pick<Dictionary["errors"], "fallback">;

/**
 * The dictionary's own sentence for an API code, or undefined for a code this
 * build does not know. Only the section's own keys count: a code is text the
 * API chose, and `constructor` must not find what every object inherits.
 */
export function knownMessage(code: string, errors: Errors): string | undefined {
  return Object.hasOwn(errors, code) ? (errors as Record<string, string>)[code] : undefined;
}

/**
 * The error text for an API code, from the active dictionary. The server's
 * English `message` never reaches the interface. An unknown code (newer than
 * this build) gets the fallback sentence. Takes `dict.errors`, or a part of it
 * that keeps the fallback, so screens with a scoped dictionary can call it.
 */
export function messageForCode(code: string, errors: Errors): string {
  return knownMessage(code, errors) ?? errors.fallback;
}
