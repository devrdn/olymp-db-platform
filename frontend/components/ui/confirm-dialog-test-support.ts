import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, vi } from "vitest";

import { CONFIRM_ARM_MS } from "./use-confirm";

// A question waits for the dialog's chunk, and its first load in a test file
// can outlast a `findBy` on a busy machine, so the file loads it before any
// test runs. The actual module, because a test may mock it to hold the load.
await vi.importActual("./confirm-dialog");

/** Presses the confirmation's answer once it is armed. */
export async function answerYes(answer: string): Promise<void> {
  const button = await screen.findByRole("button", { name: answer });
  await waitFor(() => expect(button).toBeEnabled(), { timeout: CONFIRM_ARM_MS + 1000 });
  await userEvent.click(button);
}
