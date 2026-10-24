/**
 * Reading the SQL editor's completion popup, for the tests of the editor and
 * of the screens built on it.
 */
import { screen, waitFor } from "@testing-library/react";

import { INTERACTION_DELAY_MS } from "./sql-completion";

/** The labels the open popup offers, without the column types beside them. */
export function offeredCompletions(): string[] {
  return screen
    .queryAllByRole("option")
    .map((option) => option.querySelector(".cm-completionLabel")?.textContent ?? "");
}

/** Waits until the popup offers `label`. */
export async function waitForCompletion(label: string): Promise<void> {
  await waitFor(() => {
    const offered = offeredCompletions();
    if (!offered.includes(label)) throw new Error(`the popup offers ${JSON.stringify(offered)}, not "${label}"`);
  });
}

/**
 * Waits until the popup offers `label` and a key may accept it. CodeMirror
 * ignores an accepting key for `INTERACTION_DELAY_MS` after the popup opens,
 * so a test that presses Tab or Enter sooner proves nothing about the choice.
 */
export async function waitForAcceptableCompletion(label: string): Promise<void> {
  await waitForCompletion(label);
  await new Promise((resolve) => setTimeout(resolve, INTERACTION_DELAY_MS + 25));
}
