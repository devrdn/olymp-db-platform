/**
 * Reading the SQL editor's completion popup, for the tests of the editor and
 * of the screens built on it.
 */
import { act, screen } from "@testing-library/react";
import { vi } from "vitest";

import { INTERACTION_DELAY_MS } from "./sql-completion";

/** The labels the open popup offers, without the column types beside them. */
export function offeredCompletions(): string[] {
  return screen
    .queryAllByRole("option")
    .map((option) => option.querySelector(".cm-completionLabel")?.textContent ?? "");
}

/** CodeMirror's default `activateOnTypingDelay`, which `sql-completion.ts` leaves as it is. */
const TYPING_DELAY_MS = 100;

/**
 * How far a fake clock goes for a key that accepts to work: the popup opens a
 * typing delay after the last key, and CodeMirror ignores an accepting key for
 * `INTERACTION_DELAY_MS` after that. The margin covers the pass between.
 */
const ACCEPTABLE_AFTER_MS = TYPING_DELAY_MS + INTERACTION_DELAY_MS + 25;

/** For a test on a fake clock: lets the popup open and a key accept, then checks it offers `label`. */
export async function offersAfterTyping(label: string): Promise<void> {
  await act(() => vi.advanceTimersByTimeAsync(ACCEPTABLE_AFTER_MS));
  const offered = offeredCompletions();
  if (!offered.includes(label)) throw new Error(`the popup offers ${JSON.stringify(offered)}, not "${label}"`);
}
