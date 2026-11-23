import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, test } from "vitest";

import { type Question, useConfirm } from "./use-confirm";
import { answerYes } from "./confirm-dialog-test-support";

const asked: Question = {
  title: "Ștergeți ciorna?",
  detail: "Nu se poate anula.",
  answer: "Ștergeți",
  cancel: "Anulați",
  danger: true,
};

function Asker() {
  const [ask, dialog] = useConfirm();
  const [answers, setAnswers] = useState<boolean[]>([]);
  return (
    <>
      <button type="button" onClick={() => void ask(asked).then((yes) => setAnswers((a) => [...a, yes]))}>
        ask
      </button>
      <p>answers: {answers.join(",")}</p>
      {dialog}
    </>
  );
}

describe("useConfirm", () => {
  test("asks in the caller's words, and its buttons are the caller's", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));

    const dialog = await screen.findByRole("dialog", { name: asked.title });
    expect(dialog).toHaveTextContent(asked.detail!);
    expect(screen.getByRole("button", { name: asked.answer })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: asked.cancel }).length).toBeGreaterThan(0);
  });

  test("answers no on cancel", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));
    await userEvent.click((await screen.findAllByRole("button", { name: asked.cancel }))[0]);

    expect(await screen.findByText("answers: false")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  test("answers no on Escape", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");

    expect(await screen.findByText("answers: false")).toBeInTheDocument();
  });

  test("holds the answer at first, so the second click of a double-click cannot press it", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));

    expect(await screen.findByRole("button", { name: asked.answer })).toBeDisabled();
  });

  test("answers yes once on the answer", async () => {
    render(<Asker />);

    await userEvent.click(screen.getByRole("button", { name: "ask" }));
    await answerYes(asked.answer);

    expect(await screen.findByText("answers: true")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
