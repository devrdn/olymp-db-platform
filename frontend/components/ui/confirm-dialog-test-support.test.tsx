import { getConfig, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test, vi } from "vitest";

import { answerYes } from "./confirm-dialog-test-support";
import { type Question, useConfirm } from "./use-confirm";

// The dialog's code takes longer to load than a `findBy` waits, as its first
// load does on a busy machine.
vi.mock("@/components/ui/dialog", async (importOriginal) => {
  await new Promise((resolve) => setTimeout(resolve, getConfig().asyncUtilTimeout + 500));
  return importOriginal();
});

const asked: Question = { title: "Delete the draft?", answer: "Delete", cancel: "Cancel", danger: true };

function Asker() {
  const [ask, dialog] = useConfirm();
  const [answer, setAnswer] = useState<boolean | null>(null);
  return (
    <>
      <button type="button" onClick={() => void ask(asked).then(setAnswer)}>
        ask
      </button>
      <p>answer: {String(answer)}</p>
      {dialog}
    </>
  );
}

describe("a test that answers the question", () => {
  test("finds it however slowly the dialog's code loads", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));
    await answerYes(asked.answer);

    expect(await screen.findByText("answer: true")).toBeInTheDocument();
  });
});
