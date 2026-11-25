import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
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

/** Lets what is already due on the fake clock, and the promises behind it, run. */
export const flushFakeClock = () => act(() => vi.advanceTimersByTimeAsync(0));

/**
 * Runs `body` on a fake clock, which the question's arming then costs nothing
 * on: the real 400 ms, and every wait after it, is time a loaded machine can
 * outlast. Inside it nothing may poll (`findBy`, `waitFor`): their timers are
 * the fake ones, so a test settles and reads instead.
 */
export async function onFakeClock(body: () => Promise<void>): Promise<void> {
  vi.useFakeTimers();
  try {
    await body();
  } finally {
    vi.useRealTimers();
  }
}

/**
 * Answers the open question with a yes on the fake clock, once it is armed.
 * `question` is the dialog's title, to check which question is open.
 */
export async function answerYesOnFakeClock(answer: string, question?: string): Promise<void> {
  await flushFakeClock();
  const dialog = screen.getByRole("dialog", question ? { name: question } : {});
  await act(() => vi.advanceTimersByTimeAsync(CONFIRM_ARM_MS));
  fireEvent.click(within(dialog).getByRole("button", { name: answer }));
  await flushFakeClock();
}
