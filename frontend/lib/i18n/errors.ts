import type { Dictionary } from "./dictionary";

/**
 * The error text for an API code, from the active dictionary. The server's
 * English `message` never reaches the interface. An unknown code (newer than
 * this build) gets the fallback sentence. Takes `dict.errors`, or a part of it
 * that keeps the fallback, so screens with a scoped dictionary can call it.
 */
export function messageForCode(code: string, errors: Pick<Dictionary["errors"], "fallback">): string {
  return (errors as Record<string, string>)[code] ?? errors.fallback;
}
