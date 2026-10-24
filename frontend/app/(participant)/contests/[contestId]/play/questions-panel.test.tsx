import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { OpenPlayQuestion } from "@/lib/api/play";

import { QuestionsPanel, type QuestionEntry } from "./questions-panel";
import type { AnswerState, QuestionsRefreshResult } from "./requests";
import { pasteTargetOf } from "./use-signals";

// The two actions are the boundary; what lies behind them is tested where
// it lives.
const answer = vi.hoisted(() => ({ current: { kind: "idle" } as AnswerState }));
const refresh = vi.hoisted(() => ({
  current: { kind: "ok", items: [] } as QuestionsRefreshResult,
  calls: 0,
}));

vi.mock("./requests", () => ({
  submitAnswer: async () => answer.current,
  refreshQuestions: async () => {
    refresh.calls++;
    return refresh.current;
  },
}));

// The wording of a question that opens after the page loaded is rendered in
// the browser; the stand-in marks which bodies took that path.
// A body named `NEVER_LOADS` suspends for good, standing in for a chunk
// still on its way.
const NEVER_LOADS = "never loads";
vi.mock("./question-body", () => ({
  QuestionBody: ({ markdown }: { markdown: string }) => {
    if (markdown === "never loads") throw new Promise(() => {});
    return <div data-testid="opened-body">{markdown}</div>;
  },
}));

function question(overrides: Partial<OpenPlayQuestion> = {}): OpenPlayQuestion {
  return {
    locked: false,
    id: "q1",
    kind: "text",
    points: 10,
    isBonus: false,
    choiceIds: [],
    title: "",
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
function entry(overrides: Partial<OpenPlayQuestion> = {}, index = 1): QuestionEntry {
  const q = question(overrides);
  return { question: q, index, body: <span>{q.bodyMd}</span> };
}

/** A question the server withheld: a number, no content and no body. */
function lockedEntry(position: number): QuestionEntry {
  return { question: { locked: true, position }, index: position, body: null };
}

/** The list of questions not open yet, found by its accessible name. */
function lockedList() {
  return screen.getByRole("list", { name: en.participant.play.questions.lockedList });
}

async function submit(value = "the gardener") {
  const field = screen.getByRole("textbox");
  await userEvent.type(field, value);
  await userEvent.click(screen.getByRole("button", { name: en.participant.play.questions.submit }));
}

describe("the questions panel", () => {
  test("says there is nothing to answer yet rather than rendering an empty list", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[]} dict={en} />);

    expect(screen.getByText(en.participant.play.questions.empty)).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  test("shows the question's own rendered wording and its points", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

    expect(screen.getByText(/Who was in the greenhouse/)).toBeInTheDocument();
    expect(screen.getByText("10 pts")).toBeInTheDocument();
  });

  // A submission settles on a later microtask than `userEvent.click`, so
  // every assertion on what it settled to waits (`waitFor`); asserting at
  // once failed under CPU contention.
  test("a correct answer shows the verdict and the points awarded", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 2, closed: false } };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ attemptsRemaining: 3 })]} dict={en} />);

    await submit();

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Correct! +10 points."));
  });

  test("attempts left comes from the submission, not from the page's own load", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    await waitFor(() => expect(screen.getByText("1 attempts left")).toBeInTheDocument());
  });

  test("a wrong guess clears the field rather than leaving it under the verdict", async () => {
    answer.current = {
      kind: "answer",
      result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false },
    };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    await waitFor(() => expect(screen.getByRole("textbox")).toHaveValue(""));
  });

  test("a closed question shows no form, and keeps the verdict that closed it", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = { kind: "ok", items: [question({ attemptsRemaining: 0, closed: true })] };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ attemptsRemaining: 1 })]} dict={en} />);

    await submit();

    // The one assertion gated on the settle; the two after read the same
    // render.
    await waitFor(() => expect(screen.queryByRole("textbox")).not.toBeInTheDocument());
    expect(screen.getByText(en.participant.play.questions.closed)).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Correct!");
  });

  // Either way a question closes, the re-read brings the next one in full;
  // the body already on screen is kept rather than rendered again.
  test.each([
    { how: "answered correctly", correct: true },
    { how: "out of attempts", correct: false },
  ])("closing a question ($how) re-reads the list and opens the next one in full", async ({ correct }) => {
    answer.current = {
      kind: "answer",
      result: { correct, pointsAwarded: correct ? 10 : 0, attemptsRemaining: 0, closed: true },
    };
    refresh.current = {
      kind: "ok",
      items: [
        question({ id: "q1", closed: true, canAnswer: false, correct }),
        question({ id: "q2", title: "", bodyMd: "Name the hour." }),
        { locked: true, position: 3 },
      ],
    };
    refresh.calls = 0;
    render(
      <QuestionsPanel locale="en" contestId="c1" items={[entry({ id: "q1" }), lockedEntry(2), lockedEntry(3)]} dict={en} />,
    );

    await submit();

    // Two settles: the submission, then the re-read an Effect starts after it
    // commits.
    expect(await screen.findByTestId("opened-body")).toHaveTextContent("Name the hour.");
    expect(screen.getByText("Who was in the greenhouse?").tagName).toBe("SPAN");
    expect(screen.getByRole("textbox")).toBeEnabled();
    expect(within(lockedList()).getAllByRole("listitem")).toHaveLength(1);
    expect(within(lockedList()).getByRole("listitem")).toHaveTextContent(
      en.participant.play.questions.lockedItem.replace("{n}", "3").replace("{m}", "2"),
    );
    expect(refresh.calls).toBe(1);
  });

  test("the last question opening takes the list of locked ones away", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = {
      kind: "ok",
      items: [question({ id: "q1", closed: true, canAnswer: false, correct: true }), question({ id: "q2" })],
    };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ id: "q1" }), lockedEntry(2)]} dict={en} />);

    await submit();

    await screen.findByTestId("opened-body");
    expect(screen.queryByRole("list", { name: en.participant.play.questions.lockedList })).not.toBeInTheDocument();
  });

  test("a question still loading its wording shows a placeholder hidden from assistive technology", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = {
      kind: "ok",
      items: [question({ id: "q1", closed: true, canAnswer: false, correct: true }), question({ id: "q2", bodyMd: NEVER_LOADS })],
    };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ id: "q1" }), lockedEntry(2)]} dict={en} />);

    await submit();

    await waitFor(() =>
      expect(screen.queryByRole("list", { name: en.participant.play.questions.lockedList })).not.toBeInTheDocument(),
    );
    const placeholder = document.querySelector(".animate-pulse");
    expect(placeholder).toHaveAttribute("aria-hidden", "true");
    expect(screen.queryByTestId("opened-body")).not.toBeInTheDocument();
  });

  test("a locked question is numbered as the server numbers it", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 10, attemptsRemaining: 0, closed: true } };
    refresh.current = {
      kind: "ok",
      items: [question({ id: "q1", closed: true, canAnswer: false, correct: true }), question({ id: "q2" }), { locked: true, position: 7 }],
    };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ id: "q1" }), lockedEntry(2), lockedEntry(3)]} dict={en} />);

    await submit();

    await screen.findByTestId("opened-body");
    expect(within(lockedList()).getByRole("listitem")).toHaveTextContent(
      en.participant.play.questions.lockedItem.replace("{n}", "7").replace("{m}", "6"),
    );
  });

  // The server may still say an open question cannot be answered; the form
  // then stays visible and refuses input.
  test("a question the server says cannot be answered yet keeps its field disabled", () => {
    render(
      <QuestionsPanel
        locale="en"
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
        locale="en"
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
        locale="en"
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
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    await waitFor(() => {
      const statuses = screen.getAllByRole("status");
      expect(within(statuses[statuses.length - 1]).getByText(en.errors.question_not_open)).toBeInTheDocument();
    });
  });

  test("a value refused as not one of the options says so in a sentence, not the fallback", async () => {
    answer.current = { kind: "refused", code: "answer_not_a_choice" };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

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
        locale="en"
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
        locale="en"
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
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

    await submit("the gardener");

    expect(screen.getByRole("textbox")).toHaveValue("the gardener");
  });

  // A wait, not a fault: quiet, no reference, the text kept.
  test("answering too often reads as a wait and keeps what the student typed", async () => {
    answer.current = { kind: "refused", code: "answer_too_often", requestId: "req-7" };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

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
    const { unmount } = render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);
    await submit("the gardener");
    await screen.findByText(en.errors.answer_too_long);
    expect(screen.queryByText(/req-7/)).not.toBeInTheDocument();
    unmount();

    answer.current = { kind: "refused", code: "internal_error", requestId: "req-8" };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);
    await submit("the gardener");
    expect(await screen.findByText(/req-8/)).toBeInTheDocument();
  });

  // Loaded from the server with no live submission, it still shows the
  // verdict and the points.
  test("a closed question loaded from the server shows what it was won for", () => {
    render(
      <QuestionsPanel
        locale="en"
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
        locale="en"
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
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

    await submit();

    expect(await screen.findByText(en.participant.play.questions.refreshFailed)).toBeInTheDocument();
  });
});

/** Each question's state as a tag, so the current one is visible at a glance. */
describe("a question's state", () => {
  const t = en.participant.play.questions.status;

  function threeQuestions(
    ...overrides: [Partial<OpenPlayQuestion>, Partial<OpenPlayQuestion>, Partial<OpenPlayQuestion>]
  ) {
    return overrides.map((o, i) => entry({ id: `q${i + 1}`, ...o }, i + 1));
  }

  test("marks the first still-open question as the one being worked on", () => {
    render(
      <QuestionsPanel
        locale="en"
        contestId="c1"
        items={[
          entry({ id: "q1", closed: true, correct: true }, 1),
          entry({ id: "q2", closed: false, canAnswer: true }, 2),
          lockedEntry(3),
        ]}
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
        locale="en"
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


  // After the deadline or before opening, the server marks every question
  // unanswerable but open; "current" would point at impossible work.
  test("marks nothing as current when the server says nothing can be answered", () => {
    render(
      <QuestionsPanel
        locale="en"
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
    // Nor the row, which is what assistive technology hears.
    expect(document.querySelector('[aria-current="step"]')).toBeNull();
  });

  test("an olympiad with everything answered marks nothing as current", () => {
    render(
      <QuestionsPanel
        locale="en"
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
      <QuestionsPanel locale="en" contestId="c1" items={[entry()]} scoring="icpc" icpcPenaltyMin={20} dict={en} />,
    );

    expect(screen.queryByText("10 pts")).not.toBeInTheDocument();
    expect(
      screen.getByText(en.participant.play.questions.icpcPenalty.replace("{n}", "20")),
    ).toBeInTheDocument();
  });

  test("says nothing about points in the other two scorings", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} scoring="points" icpcPenaltyMin={20} dict={en} />);

    expect(
      screen.queryByText(en.participant.play.questions.icpcPenalty.replace("{n}", "20")),
    ).not.toBeInTheDocument();
  });

  test("a correct answer says so without a point value", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 0, attemptsRemaining: 2, closed: false } };
    render(
      <QuestionsPanel
        locale="en"
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
        locale="en"
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
  render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);
  expect(pasteTargetOf(screen.getByRole("textbox", { name: en.participant.play.questions.answerLabel }))).toBe("answer");
});

// A bonus question gives back penalty points rather than earning its own
// (docs/ARCHITECTURE.md §6.1.1), so it never reads as "+0 points".
describe("a bonus question", () => {
  const t = en.participant.play.questions;

  test("is marked, and says its points are given back rather than earned", () => {
    render(
      <QuestionsPanel locale="en" contestId="c1" items={[entry({ isBonus: true, points: 4 }), entry({ id: "q2" }, 2)]} dict={en} />,
    );

    expect(screen.getAllByText(t.bonus)).toHaveLength(1);
    expect(screen.getByText(t.bonusPoints.replace("{n}", "4"))).toBeInTheDocument();
    expect(screen.getByText(t.bonusHint.replace("{n}", "4"))).toBeInTheDocument();
    expect(screen.queryByText("4 pts")).not.toBeInTheDocument();
    // The regular question beside it keeps its plain points.
    expect(screen.getByText("10 pts")).toBeInTheDocument();
  });

  test("a correct answer says lost points are given back, not that it earned nothing", async () => {
    answer.current = { kind: "answer", result: { correct: true, pointsAwarded: 0, attemptsRemaining: 2, closed: false } };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ isBonus: true, points: 4 })]} dict={en} />);

    await submit();

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(t.correctBonus.replace("{n}", "4")));
    expect(screen.queryByText(/\+0 points/)).not.toBeInTheDocument();
  });

  test("closed from an earlier visit, it keeps the same verdict", () => {
    render(
      <QuestionsPanel
        locale="en"
        contestId="c1"
        items={[entry({ isBonus: true, points: 4, closed: true, canAnswer: false, correct: true, pointsAwarded: 0 })]}
        dict={en}
      />,
    );

    expect(screen.getByRole("status")).toHaveTextContent(t.correctBonus.replace("{n}", "4"));
    // The invitation to answer is for an open question only.
    expect(screen.queryByText(t.bonusHint.replace("{n}", "4"))).not.toBeInTheDocument();
    expect(screen.getByText(t.bonus)).toBeInTheDocument();
  });

  test("a wrong answer reads as a plain miss", async () => {
    answer.current = { kind: "answer", result: { correct: false, pointsAwarded: 0, attemptsRemaining: 1, closed: false } };
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ isBonus: true, points: 4, attemptsRemaining: 2 })]} dict={en} />);

    await submit("wrong guess");

    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(t.incorrect));
  });

  // The server sends a locked question as a number alone, so nothing here can
  // say a bonus lies ahead.
  test("locked, it is a number like any other, with no bonus mark", () => {
    render(
      <QuestionsPanel
        locale="en"
        contestId="c1"
        items={[entry({ id: "q1" }, 1), lockedEntry(2)]}
        dict={en}
      />,
    );

    expect(within(lockedList()).queryByText(t.bonus)).not.toBeInTheDocument();
    expect(screen.queryByText(t.bonus)).not.toBeInTheDocument();
  });

  test("keeps the bonus line wrapping inside a narrow panel", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ isBonus: true, points: 4 })]} dict={en} />);

    expect(screen.getByText(t.bonusHint.replace("{n}", "4"))).toHaveClass("[overflow-wrap:anywhere]");
  });
});

// A titled question reads as its title, with the wording folded under it
// (open in place, or larger in a dialog); the answer field never folds.
describe("a question with a title", () => {
  const t = en.participant.play.questions;
  const titled = (overrides: Partial<OpenPlayQuestion> = {}, index = 1) =>
    entry({ title: "The greenhouse", ...overrides }, index);

  test("shows the title and folds the wording behind Details", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);

    expect(screen.getByText("The greenhouse")).toBeVisible();
    const details = screen.getByRole("button", { name: t.details });
    expect(details).toHaveAttribute("aria-expanded", "false");
    const region = document.getElementById(details.getAttribute("aria-controls")!);
    expect(region).not.toBeNull();
    expect(region).not.toBeVisible();
    expect(screen.getByText("Who was in the greenhouse?")).not.toBeVisible();
  });

  test("keeps the answer field reachable while the wording is folded", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);

    expect(screen.getByRole("textbox", { name: t.answerLabel })).toBeEnabled();
    expect(screen.getByRole("button", { name: t.submit })).toBeEnabled();
  });

  test("opens the wording in place and folds it again", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);
    const details = screen.getByRole("button", { name: t.details });

    await user.click(details);

    expect(details).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Who was in the greenhouse?")).toBeVisible();

    await user.click(details);

    expect(details).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("Who was in the greenhouse?")).not.toBeVisible();
  });

  test("opens from the keyboard with Enter and with Space", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);
    const details = screen.getByRole("button", { name: t.details });

    await user.tab();
    expect(details).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(details).toHaveAttribute("aria-expanded", "true");
    await user.keyboard(" ");
    expect(details).toHaveAttribute("aria-expanded", "false");
  });

  test("names which question each control belongs to", () => {
    render(
      <QuestionsPanel
        locale="en"
        contestId="c1"
        items={[titled(), titled({ id: "q2", title: "The weapon" }, 2)]}
        dict={en}
      />,
    );

    const [first, second] = screen.getAllByRole("button", { name: t.details });
    expect(first).toHaveAccessibleDescription("The greenhouse");
    expect(second).toHaveAccessibleDescription("The weapon");
    expect(screen.getAllByRole("button", { name: t.openLarger })[1]).toHaveAccessibleDescription("The weapon");
  });

  test("shows the wording larger in a dialog named by the title, and returns focus on close", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);
    const larger = screen.getByRole("button", { name: t.openLarger });

    await user.click(larger);

    const dialog = await screen.findByRole("dialog", { name: "The greenhouse" });
    expect(within(dialog).getByText("Who was in the greenhouse?")).toBeVisible();
    await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));

    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(larger).toHaveFocus());
  });

  test("keeps focus inside the dialog while it is open", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);

    await user.click(screen.getByRole("button", { name: t.openLarger }));
    const dialog = await screen.findByRole("dialog", { name: "The greenhouse" });

    // Tabbing off either end lands on a focus guard, which sends focus back in.
    for (const shift of [false, false, false, true, true, true]) {
      await user.tab({ shift });
      await waitFor(() => expect(dialog).toContainElement(document.activeElement as HTMLElement));
    }
  });

  test("closes the dialog from its own close control", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled()]} dict={en} />);

    await user.click(screen.getByRole("button", { name: t.openLarger }));
    const dialog = await screen.findByRole("dialog", { name: "The greenhouse" });
    await user.click(within(dialog).getByRole("button", { name: t.closeLarger }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  test("shows a long title whole, never cut short", () => {
    const long = "Who among the seven guests could have reached the greenhouse ".repeat(3).trim();
    render(<QuestionsPanel locale="en" contestId="c1" items={[titled({ title: long })]} dict={en} />);

    expect(screen.getByText(long)).toBeVisible();
    expect(screen.getByRole("button", { name: t.details })).toHaveAccessibleDescription(long);
  });

  test("keeps the title and the folded wording on a closed question", () => {
    render(
      <QuestionsPanel
        locale="en"
        contestId="c1"
        items={[titled({ closed: true, correct: true, pointsAwarded: 10, attemptsRemaining: 0 })]}
        dict={en}
      />,
    );

    expect(screen.getByText("The greenhouse")).toBeVisible();
    expect(screen.getByRole("button", { name: t.details })).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.getByText(t.closed)).toBeInTheDocument();
  });

  test("gives the locked questions below it nothing to open", () => {
    render(
      <QuestionsPanel locale="en" contestId="c1" items={[titled(), lockedEntry(2), lockedEntry(3)]} dict={en} />,
    );

    expect(screen.getAllByRole("button", { name: t.details })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: t.openLarger })).toHaveLength(1);
    expect(within(lockedList()).queryAllByRole("heading")).toHaveLength(0);
  });
});

describe("a question without a title", () => {
  const t = en.participant.play.questions;

  test("shows its wording as before, unfolded and with nothing to open", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry()]} dict={en} />);

    expect(screen.getByText("Who was in the greenhouse?")).toBeVisible();
    expect(screen.queryByRole("button", { name: t.details })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: t.openLarger })).not.toBeInTheDocument();
  });

  test("treats a title of spaces as none", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[entry({ title: "   " })]} dict={en} />);

    expect(screen.getByText("Who was in the greenhouse?")).toBeVisible();
    expect(screen.queryByRole("button", { name: t.details })).not.toBeInTheDocument();
  });
});

// docs/ARCHITECTURE.md §6.1.1: past the frontier the server sends a number
// and nothing else, and the screen shows exactly that.
describe("a locked question", () => {
  const t = en.participant.play.questions;
  const sequence = () => [entry({ id: "q1", closed: true, correct: true }, 1), entry({ id: "q2" }, 2), lockedEntry(3), lockedEntry(4)];

  test("is listed by number below the open ones, naming the question it waits on", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={sequence()} dict={en} />);

    const items = within(lockedList()).getAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      t.lockedItem.replace("{n}", "3").replace("{m}", "2"),
      t.lockedItem.replace("{n}", "4").replace("{m}", "3"),
    ]);
    // Below the current question, not among the open ones.
    const current = document.querySelector('[aria-current="step"]')!;
    expect(current.compareDocumentPosition(lockedList()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  test("offers no field, no points and no state of its own", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={sequence()} dict={en} />);

    // One field, for the current question; one points label per open question.
    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getAllByText(/pts$/)).toHaveLength(2);
    const list = lockedList();
    expect(within(list).queryByRole("textbox")).not.toBeInTheDocument();
    expect(within(list).queryByRole("button")).not.toBeInTheDocument();
    expect(within(list).queryByRole("link")).not.toBeInTheDocument();
    expect(within(list).queryByText(/pts/)).not.toBeInTheDocument();
  });

  test("is never a stop for the keyboard", async () => {
    const user = userEvent.setup();
    render(<QuestionsPanel locale="en" contestId="c1" items={sequence()} dict={en} />);
    const list = lockedList();

    const visited: Element[] = [];
    for (let i = 0; i < 10; i++) {
      await user.tab();
      if (document.activeElement && document.activeElement !== document.body) visited.push(document.activeElement);
    }
    expect(visited.length).toBeGreaterThan(0);
    expect(visited.some((element) => list.contains(element))).toBe(false);
    expect(list.querySelector("[tabindex]")).toBeNull();
  });

  test("is announced as text, its decoration kept from assistive technology", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={sequence()} dict={en} />);

    for (const item of within(lockedList()).getAllByRole("listitem")) {
      expect(item).not.toHaveAttribute("aria-hidden");
      for (const svg of item.querySelectorAll("svg")) expect(svg).toHaveAttribute("aria-hidden", "true");
    }
    // Only the open question being worked on is the current step.
    expect(document.querySelectorAll('[aria-current="step"]')).toHaveLength(1);
  });

  // Unreachable while the publish gate holds (no hidden question in a
  // sequential contest), but a first question with nothing before it still
  // reads sensibly.
  test("first in the list, it says only that it is not open yet", () => {
    render(<QuestionsPanel locale="en" contestId="c1" items={[lockedEntry(1), lockedEntry(2)]} dict={en} />);

    expect(within(lockedList()).getAllByRole("listitem").map((item) => item.textContent)).toEqual([
      t.lockedNotYet.replace("{n}", "1"),
      t.lockedItem.replace("{n}", "2").replace("{m}", "1"),
    ]);
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByText(t.empty)).not.toBeInTheDocument();
  });

  test("a long run of them stays one plain list", () => {
    const many = [entry({ id: "q1" }, 1), ...Array.from({ length: 40 }, (_, i) => lockedEntry(i + 2))];
    render(<QuestionsPanel locale="en" contestId="c1" items={many} dict={en} />);

    expect(within(lockedList()).getAllByRole("listitem")).toHaveLength(40);
    expect(within(lockedList()).getAllByRole("listitem")[39]).toHaveTextContent(
      t.lockedItem.replace("{n}", "41").replace("{m}", "40"),
    );
  });
});
