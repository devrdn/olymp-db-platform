import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect } from "vitest";

import { CONFIRM_ARM_MS } from "./use-confirm";

/** Presses the confirmation's answer once it is armed. */
export async function answerYes(answer: string): Promise<void> {
  const button = await screen.findByRole("button", { name: answer });
  await waitFor(() => expect(button).toBeEnabled(), { timeout: CONFIRM_ARM_MS + 1000 });
  await userEvent.click(button);
}
