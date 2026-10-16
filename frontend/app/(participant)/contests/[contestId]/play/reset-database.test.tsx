import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

import type { ResetState } from "./actions";

// The real Server Actions module would pull in Next's server runtime.
const { resetDatabaseAction } = vi.hoisted(() => ({
  resetDatabaseAction: vi.fn(async (previous: ResetState, form: FormData): Promise<ResetState> => {
    void previous;
    void form;
    return { kind: "done" };
  }),
}));
vi.mock("./actions", () => ({ resetDatabaseAction }));

import { ResetDatabase } from "./reset-database";

const t = en.participant.play.workspace.reset;
const contestId = "11111111-1111-1111-1111-111111111111";

beforeEach(() => {
  resetDatabaseAction.mockClear();
});

function show(onReset = vi.fn()) {
  render(<ResetDatabase contestId={contestId} onReset={onReset} dict={en} />);
  return onReset;
}

async function openAndConfirm() {
  await userEvent.click(screen.getByRole("button", { name: t.open }));
  await userEvent.click(screen.getByRole("button", { name: t.confirm }));
}

describe("ResetDatabase", () => {
  test("asks first, saying the tables return to the start and the participant's changes are lost", async () => {
    show();

    await userEvent.click(screen.getByRole("button", { name: t.open }));

    const dialog = screen.getByRole("dialog", { name: t.title });
    expect(dialog).toHaveTextContent(t.body);
    expect(dialog).toHaveTextContent(t.kept);
    expect(resetDatabaseAction).not.toHaveBeenCalled();
  });

  test("cancelling resets nothing", async () => {
    const onReset = show();

    await userEvent.click(screen.getByRole("button", { name: t.open }));
    await userEvent.click(screen.getByRole("button", { name: t.cancel }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(resetDatabaseAction).not.toHaveBeenCalled();
    expect(onReset).not.toHaveBeenCalled();
  });

  test("confirming resets this contest's database, says so and tells the screen", async () => {
    const onReset = show();

    await openAndConfirm();

    expect(await screen.findByRole("status")).toHaveTextContent(t.done);
    expect(resetDatabaseAction).toHaveBeenCalledTimes(1);
    expect(resetDatabaseAction.mock.calls[0][1].get("contestId")).toBe(contestId);
    expect(onReset).toHaveBeenCalledTimes(1);
  });

  test("a refusal is shown in the participant's language and changes nothing on screen", async () => {
    resetDatabaseAction.mockResolvedValueOnce({ kind: "refused", code: "reset_too_often", requestId: "req-7" });
    const onReset = show();

    await openAndConfirm();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.errors.reset_too_often);
    // An ordinary refusal: a reference would read as a fault.
    expect(alert).not.toHaveTextContent("req-7");
    expect(onReset).not.toHaveBeenCalled();
  });

  test("a fault on our side carries the request reference", async () => {
    resetDatabaseAction.mockResolvedValueOnce({ kind: "refused", code: "internal_error", requestId: "req-9" });
    show();

    await openAndConfirm();

    expect(await screen.findByRole("alert")).toHaveTextContent(
      en.participant.console.reference.replace("{id}", "req-9"),
    );
  });

  test("cannot be pressed twice while a reset is in flight", async () => {
    let finish: (state: ResetState) => void = () => {};
    resetDatabaseAction.mockImplementationOnce(
      () => new Promise<ResetState>((resolve) => (finish = resolve)),
    );
    show();

    await openAndConfirm();

    const pressed = await screen.findByRole("button", { name: t.resetting });
    expect(pressed).toBeDisabled();
    expect(screen.getByRole("button", { name: t.cancel })).toBeDisabled();
    finish({ kind: "done" });
    expect(await screen.findByRole("status")).toHaveTextContent(t.done);
  });

  test("opened again, it asks again rather than showing the last outcome", async () => {
    show();

    await openAndConfirm();
    await screen.findByRole("status");
    // The footer's button; the dialog's corner control shares its name.
    await userEvent.click(screen.getAllByRole("button", { name: t.close }).at(-1)!);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: t.open }));

    expect(screen.getByRole("button", { name: t.confirm })).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
