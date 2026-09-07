import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { PlayQuestion } from "@/lib/api/play";

import { QuestionsPanel, type QuestionEntry } from "./questions-panel";
import type { AnswerState, QuestionsRefreshResult } from "./actions";

// The two actions are the boundary: what they return is what the panel has
// to render, and everything behind them (the server action itself, the API
// call) is tested where it lives.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as AnswerState }));
const refresh = vi.hoisted(() => ({
  current: { kind: "ok", items: [] } as QuestionsRefreshResult,
  calls: 0,
}));

vi.mock("./actions", () => ({
  submitAnswerAction: async () => answer.current,
  refreshQuestionsAction: async () => {
    refresh.calls++;
    return refresh.current;
  },
}));

function question(overrides: Partial<PlayQuestion> = {}): PlayQuestion {
  return {
    id: "q1",
    kind: "text",
    points: 10,
    choiceIds: [],
    bodyMd: "Who was in the greenhouse?",
    choices: {},
    attemptsRemaining: undefined,
    closed: false,
    canAnswer: true,
    ...overrides,
  };
}

/**
 * The panel never runs Markdown itself (questions-panel.tsx's own doc): a
 * Server Component renders the body once and hands the panel the result.
 * This fake stands in for that render — a plain span carrying the question's
 * own wording — so a test can still find it by text.
 */
function entry(overrides: Partial<PlayQuestion> = {}): QuestionEntry {
  const q = question(overrides);
  return { question: q, body: <span>{q.bodyMd}</span> };
}

async function submit(value = "the gardener") {
  const field = screen.getByRole("textbox");
  await userEvent.type(field, value);
  await userEvent.click(screen.getByRole("button", { name: en.participant.play.questions.submit }));
}

describe("the questions panel", () => {
  test("says there is nothing to answer yet rather than rendering an empty list", () => {
    render(<QuestionsPanel contestId="c1" items={[]} dict={en} />);

    expect(screen.getByText(en.participant.play.questions.empty)).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  test("shows the question's own rendered wording and its points", () => {
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    expect(screen.getByText(/Who was in the greenhouse/)).toBeInTheDocument();
    expect(screen.getByText("10 pts")).toBeInTheDocument();
  });

  test("a correct answer shows the verdict and the points awarded", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 2, closed: false } };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 3 })]} dict={en} />);

    await submit();

    expect(screen.getByRole("status")).toHaveTextContent("Correct! +10 points.");
  });

  test("attempts left comes from the submission, not from the page's own load", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    expect(screen.getByText("1 attempts left")).toBeInTheDocument();
  });

  test("a wrong guess clears the field rather than leaving it under the verdict", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    expect(screen.getByRole("textbox")).toHaveValue("");
  });

  test("a closed question shows no form, and keeps the verdict that closed it", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = { kind: "ok", items: [question({ attemptsRemaining: 0, closed: true })] };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 1 })]} dict={en} />);

    await submit();

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText(en.participant.play.questions.closed)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Correct!");
  });

  test("closing a question re-reads the list, so a locked one can open — without asking react-markdown to run again", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: true, pointsAwarded: 10, attemptsRemaining: undefined, closed: true },
    };
    // The refetch reports q1 closed and q2 now open — a sequential contest
    // moving on. Neither item's own wording is part of this response; the
    // rendered body each entry already carries must survive untouched.
    refresh.current = {
      kind: "ok",
      items: [question({ id: "q1", closed: true }), question({ id: "q2", canAnswer: true })],
    };
    refresh.calls = 0;
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ id: "q1" }), entry({ id: "q2", canAnswer: false, bodyMd: "Name the hour." })]}
        dict={en}
      />,
    );

    await userEvent.type(screen.getAllByRole("textbox")[0], "the gardener");
    await userEvent.click(screen.getAllByRole("button", { name: en.participant.play.questions.submit })[0]);

    expect(await screen.findByText(en.participant.play.questions.closed)).toBeInTheDocument();
    expect(screen.queryByText(en.participant.play.questions.locked)).not.toBeInTheDocument();
    expect(screen.getByText(/Name the hour/)).toBeInTheDocument();
    expect(refresh.calls).toBe(1);
  });

  test("a question not yet open in sequence is shown but cannot be answered", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ canAnswer: false, closed: false })]}
        dict={en}
      />,
    );

    expect(screen.getByText(en.participant.play.questions.locked)).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toBeDisabled();
    expect(screen.getByRole("button", { name: en.participant.play.questions.submit })).toBeDisabled();
  });

  test("a choice question offers its own labelled options, never free text", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[
          entry({
            kind: "choice",
            choiceIds: ["a", "b"],
            choices: { a: "The butler", b: "The gardener" },
          }),
        ]}
        dict={en}
      />,
    );

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "The butler" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "The gardener" })).toBeInTheDocument();
  });

  test("a refusal names what it was about, from the caller's own words", async () => {
    answer.current = { kind: "refused", code: "question_not_open" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    const statuses = screen.getAllByRole("status");
    expect(within(statuses[statuses.length - 1]).getByText(en.errors.question_not_open)).toBeInTheDocument();
  });

  test("two questions answer independently: one closing does not touch what the other is holding", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: true, pointsAwarded: 5, attemptsRemaining: undefined, closed: true },
    };
    // The refetch this triggers reports both still open, the way a
    // non-sequential contest would: nothing about the second question
    // changed, so its own field must not be reset by the first one's refresh.
    refresh.current = {
      kind: "ok",
      items: [question({ id: "q1" }), question({ id: "q2" })],
    };
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ id: "q1" }), entry({ id: "q2", bodyMd: "Name the hour." })]}
        dict={en}
      />,
    );

    const fields = screen.getAllByRole("textbox");
    await userEvent.type(fields[1], "midnight");
    await userEvent.type(fields[0], "the gardener");
    await userEvent.click(screen.getAllByRole("button", { name: en.participant.play.questions.submit })[0]);

    await screen.findByText("Correct!", { exact: false });
    // Only the second question still shows a field — the first closed — and
    // it must still hold what was typed before the first one was submitted.
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getByRole("textbox")).toHaveValue("midnight");
  });
});
