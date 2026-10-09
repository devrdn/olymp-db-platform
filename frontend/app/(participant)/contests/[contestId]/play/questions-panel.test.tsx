import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { PlayQuestion } from "@/lib/api/play";

import { QuestionsPanel, type QuestionEntry } from "./questions-panel";
import type { AnswerState, QuestionsRefreshResult } from "./actions";
import { pasteTargetOf } from "./use-signals";

// The two actions are the boundary; what lies behind them is tested where
// it lives.
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
    correct: false,
    pointsAwarded: 0,
    ...overrides,
  };
}

/** A question entry whose body is a plain span, standing in for the server render. */
function entry(overrides: Partial<PlayQuestion> = {}, index = 1): QuestionEntry {
  const q = question(overrides);
  return { question: q, index, body: <span>{q.bodyMd}</span> };
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

  // A submission settles on a later microtask than `userEvent.click`, so
  // every assertion on what it settled to waits (`waitFor`); asserting at
  // once failed under CPU contention.
  test("a correct answer shows the verdict and the points awarded", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 2, closed: false } };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 3 })]} dict={en} />);

    await submit();

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Correct! +10 points."));
  });

  test("attempts left comes from the submission, not from the page's own load", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    await waitFor(() => expect(screen.getByText("1 attempts left")).toBeInTheDocument());
  });

  test("a wrong guess clears the field rather than leaving it under the verdict", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  });

  test("a closed question shows no form, and keeps the verdict that closed it", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = { kind: "ok", items: [question({ attemptsRemaining: 0, closed: true })] };
    render(<QuestionsPanel contestId="c1" items={[entry({ attemptsRemaining: 1 })]} dict={en} />);

    await submit();

    // The one assertion gated on the settle; the two after read the same
    // render.
    await waitFor(() => expect(screen.queryByRole("textbox")).not.toBeInTheDocument());
    expect(screen.getByText(en.participant.play.questions.closed)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Correct!");
  });

  test("closing a question re-reads the list, so a locked one can open — without asking react-markdown to run again", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: true, pointsAwarded: 10, attemptsRemaining: undefined, closed: true },
    };
    // q1 closed and q2 open, as a sequential contest moves on; each entry's
    // rendered body must survive the re-read.
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
    // Two settles: q1's submission, then the re-read an Effect starts after
    // it commits. `findByText` above waits only for the first.
    await waitFor(() => expect(screen.queryByText(en.participant.play.questions.locked)).not.toBeInTheDocument());
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

  // The 16px radio is what the design draws; the label is the hit area and
  // must reach the 24px a thumb needs.
  test("a choice's own label is the hit area, and is kept at least 24px tall", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ kind: "choice", choiceIds: ["a"], choices: { a: "The butler" } })]}
        dict={en}
      />,
    );

    const label = screen.getByRole("radio", { name: "The butler" }).closest("label");
    expect(label).not.toBeNull();
    expect(label!.className).toMatch(/(^|\s)min-h-6(\s|$)/);
  });

  test("a refusal names what it was about, from the caller's own words", async () => {
    answer.current = { kind: "refused", code: "question_not_open" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    await waitFor(() => {
      const statuses = screen.getAllByRole("status");
      expect(within(statuses[statuses.length - 1]).getByText(en.errors.question_not_open)).toBeInTheDocument();
    });
  });

  test("a value refused as not one of the options says so in a sentence, not the fallback", async () => {
    answer.current = { kind: "refused", code: "answer_not_a_choice" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    await waitFor(() => {
      const statuses = screen.getAllByRole("status");
      const last = statuses[statuses.length - 1];
      expect((en.errors as Record<string, string>).answer_not_a_choice).toBeTruthy();
      expect(within(last).getByText((en.errors as Record<string, string>).answer_not_a_choice)).toBeInTheDocument();
    });
  });

  test("two questions answer independently: one closing does not touch what the other is holding", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: true, pointsAwarded: 5, attemptsRemaining: undefined, closed: true },
    };
    // Both still open, as in a non-sequential contest: the refresh must not
    // reset the second question's field.
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
    // Only the second still has a field, holding what was typed.
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getByRole("textbox")).toHaveValue("midnight");
  });

  // Spliced in front of the Markdown, the number would break a question
  // opening with a heading, a list or a fenced block.
  test("shows the question's own display number as an element separate from its wording", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({}, 1), entry({ id: "q2", bodyMd: "Name the hour." }, 2)]}
        dict={en}
      />,
    );

    expect(screen.getByText("1.")).toBeInTheDocument();
    expect(screen.getByText("2.")).toBeInTheDocument();
    expect(screen.getByText(/Who was in the greenhouse/)).toBeInTheDocument();
    expect(screen.getByText(/Name the hour/)).toBeInTheDocument();
  });

  // These refusals say "try again", so the typed text must still be there.
  test("a refusal that says try again leaves what the student typed in the field", async () => {
    answer.current = { kind: "refused", code: "attempt_conflict" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit("the gardener");

    expect(screen.getByRole("textbox")).toHaveValue("the gardener");
  });

  // A wait, not a fault: quiet, no reference, the text kept.
  test("answering too often reads as a wait and keeps what the student typed", async () => {
    answer.current = { kind: "refused", code: "answer_too_often", requestId: "req-7" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit("the gardener");

    const refusal = await screen.findByText(en.errors.answer_too_often);
    expect(refusal).not.toHaveClass("bg-bad-wash");
    expect(refusal).toHaveClass("bg-sunk");
    expect(screen.queryByText(/req-7/)).not.toBeInTheDocument();
    expect(screen.getByRole("textbox")).toHaveValue("the gardener");
  });

  // As in the console: a reference under an ordinary refusal reads as a fault.
  test("carries a reference for a fault, and not for an ordinary refusal", async () => {
    answer.current = { kind: "refused", code: "answer_too_long", requestId: "req-7" };
    const { unmount } = render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);
    await submit("the gardener");
    await screen.findByText(en.errors.answer_too_long);
    expect(screen.queryByText(/req-7/)).not.toBeInTheDocument();
    unmount();

    answer.current = { kind: "refused", code: "internal_error", requestId: "req-8" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);
    await submit("the gardener");
    expect(await screen.findByText(/req-8/)).toBeInTheDocument();
  });

  // Loaded from the server with no live submission, it still shows the
  // verdict and the points.
  test("a closed question loaded from the server shows what it was won for", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ closed: true, canAnswer: false, correct: true, pointsAwarded: 7 })]}
        dict={en}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent("Correct! +7 points.");
  });

  test("a closed question loaded from the server that was never solved says so rather than staying silent", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ closed: true, canAnswer: false, correct: false, pointsAwarded: 0 })]}
        dict={en}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.questions.incorrect);
  });

  // A sequential contest needs this re-read to unlock the next question.
  test("a refused re-read after a question closes says a reload would help, rather than staying silent", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: true, pointsAwarded: 10, attemptsRemaining: undefined, closed: true },
    };
    refresh.current = { kind: "refused", code: "unreachable" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    expect(await screen.findByText(en.participant.play.questions.refreshFailed)).toBeInTheDocument();
  });
});

/** Each question's state as a tag, so the current one is visible at a glance. */
describe("a question's state", () => {
  const t = en.participant.play.questions.status;

  function threeQuestions(
    ...overrides: [Partial<PlayQuestion>, Partial<PlayQuestion>, Partial<PlayQuestion>]
  ) {
    return overrides.map((o, i) => entry({ id: `q${i + 1}`, ...o }, i + 1));
  }

  test("marks the first still-open question as the one being worked on", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={threeQuestions(
          { closed: true, correct: true },
          { closed: false, canAnswer: true },
          { closed: false, canAnswer: false },
        )}
        dict={en}
      />,
    );

    expect(screen.getByText(t.accepted)).toBeInTheDocument();
    expect(screen.getByText(t.current)).toBeInTheDocument();
    // Exactly one is current: two would be no mark at all.
    expect(screen.queryAllByText(t.current)).toHaveLength(1);
  });

  // Both close a question but mean opposite things.
  test("tells a closed question that was answered from one that ran out of attempts", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={threeQuestions(
          { closed: true, correct: true },
          { closed: true, correct: false },
          { closed: false, canAnswer: true },
        )}
        dict={en}
      />,
    );

    expect(screen.getByText(t.accepted)).toBeInTheDocument();
    expect(screen.getByText(t.spent)).toBeInTheDocument();
  });

  // The server enforces the order (docs/ARCHITECTURE.md §6.1.1); naming the
  // blocking question beats "not yet".
  test("names the question a locked one is waiting on", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={threeQuestions(
          { closed: true, correct: true },
          { closed: false, canAnswer: true },
          { closed: false, canAnswer: false },
        )}
        dict={en}
      />,
    );

    expect(screen.getByText(t.after.replace("{n}", "2"))).toBeInTheDocument();
  });

  // After the deadline or before opening, the server marks every question
  // unanswerable but open; "current" would point at impossible work.
  test("marks nothing as current when the server says nothing can be answered", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={threeQuestions(
          { closed: false, canAnswer: false },
          { closed: false, canAnswer: false },
          { closed: false, canAnswer: false },
        )}
        dict={en}
      />,
    );

    expect(screen.queryByText(t.current)).not.toBeInTheDocument();
    // Nor the row: a locked question's tag reads "after N", so only the row's
    // mark says which question is current.
    expect(document.querySelector('[aria-current="step"]')).toBeNull();
  });

  test("an olympiad with everything answered marks nothing as current", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={threeQuestions(
          { closed: true, correct: true },
          { closed: true, correct: true },
          { closed: true, correct: false },
        )}
        dict={en}
      />,
    );

    expect(screen.queryByText(t.current)).not.toBeInTheDocument();
    expect(screen.queryAllByText(t.accepted)).toHaveLength(2);
  });
});

// docs/ARCHITECTURE.md §6.1.1: ICPC ranks by questions solved, then penalty
// time, so points are never shown and a correct answer carries no value.
describe("the questions panel, ICPC scoring", () => {
  test("hides a question's own points, and states the penalty once for the whole list", () => {
    render(
      <QuestionsPanel contestId="c1" items={[entry()]} scoring="icpc" icpcPenaltyMin={20} dict={en} />,
    );

    expect(screen.queryByText("10 pts")).not.toBeInTheDocument();
    expect(
      screen.getByText(en.participant.play.questions.icpcPenalty.replace("{n}", "20")),
    ).toBeInTheDocument();
  });

  test("says nothing about points in the other two scorings", () => {
    render(<QuestionsPanel contestId="c1" items={[entry()]} scoring="points" icpcPenaltyMin={20} dict={en} />);

    expect(
      screen.queryByText(en.participant.play.questions.icpcPenalty.replace("{n}", "20")),
    ).not.toBeInTheDocument();
  });

  test("a correct answer says so without a point value", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 0, attemptsRemaining: 2, closed: false } };
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ attemptsRemaining: 3 })]}
        scoring="icpc"
        icpcPenaltyMin={20}
        dict={en}
      />,
    );

    await submit();

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.questions.correctIcpc));
    expect(screen.getByRole("status")).not.toHaveTextContent("points");
  });

  test("a question closed from an earlier visit also shows the pointless verdict", () => {
    render(
      <QuestionsPanel
        contestId="c1"
        items={[entry({ closed: true, canAnswer: false, correct: true, pointsAwarded: 0 })]}
        scoring="icpc"
        icpcPenaltyMin={20}
        dict={en}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(en.participant.play.questions.correctIcpc);
    expect(screen.getByRole("status")).not.toHaveTextContent("points");
  });
});

test("a paste into an answer is watched as one into the answer", () => {
  render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);
  expect(pasteTargetOf(screen.getByRole("textbox", { name: en.participant.play.questions.answerLabel }))).toBe("answer");
});
