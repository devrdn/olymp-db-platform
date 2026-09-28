import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { PlayQuestion } from "@/lib/api/play";

import { QuestionsPanel, type QuestionEntry } from "./questions-panel";
import type { AnswerState, QuestionsRefreshResult } from "./actions";
import { pasteTargetOf } from "./use-signals";

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
    correct: false,
    pointsAwarded: 0,
    ...overrides,
  };
}

/**
 * The panel never runs Markdown itself (questions-panel.tsx's own doc): a
 * Server Component renders the body once and hands the panel the result.
 * This fake stands in for that render — a plain span carrying the question's
 * own wording — so a test can still find it by text.
 */
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

  // `useActionState`'s own state update settles on a later microtask than
  // `userEvent.click` awaits (console.test.tsx's own finding, for the same
  // `formAction`/`pending` shape) — asserting immediately after `submit()`
  // passed on an idle machine and failed once several `vitest run` processes
  // were contending for the same CPUs (reproduced by running this suite four
  // times in parallel). Every assertion below that reads what a submission
  // settled to is therefore wrapped in `waitFor` rather than asserted
  // straight after `submit()` returns.
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

    // The one assertion actually gated on the submission settling; the two
    // that follow read the same render once it has.
    await waitFor(() => expect(screen.queryByRole("textbox")).not.toBeInTheDocument());
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
    // Two settles, not one: q1's own submission closing is the first, and it
    // is what `findByText` above waits for — but the re-read that unlocks q2
    // is a *second* one, kicked off from an Effect that only runs after that
    // first render commits. `findByText` resolving here says nothing about
    // whether that second settle has happened yet, and asserting straight
    // after it — reliably true on an idle machine — is exactly what failed
    // under a full-suite run: q2 was still shown locked.
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

  // The radio itself is 16px, which is what the design draws; what has to be
  // hittable is the label around it, because clicking anywhere on the label
  // is what selects the choice. Measured, that row was 22px tall on every
  // screen size — under the 24px a thumb needs — so the whole answer to a
  // multiple-choice question was a strip too thin to press reliably.
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

  // Finding 6: the display number is a sibling of the question's own
  // rendered wording, never text spliced in front of the Markdown that
  // produced it — spliced text would break a question whose wording opens
  // with a heading, a list or a fenced block.
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

  // Finding 3: `attempt_conflict` and `query_too_often` both tell the student
  // to try again — remounting the form on those had already deleted what
  // they typed by the time they read the instruction.
  test("a refusal that says try again leaves what the student typed in the field", async () => {
    answer.current = { kind: "refused", code: "attempt_conflict" };
    render(<QuestionsPanel contestId="c1" items={[entry()]} dict={en} />);

    await submit("the gardener");

    expect(screen.getByRole("textbox")).toHaveValue("the gardener");
  });

  // Answering too often is a wait, not a fault: shown in the same quiet
  // style as a query sent too often, with no request reference to report and
  // what the student typed still in the field for when the minute is out.
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

  // Finding 5: a closed question loaded fresh from the server — no live
  // submission behind it — must still say whether it was won, and for how
  // much, the same way one just answered in this session does.
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

  // Finding 6: a sequential contest's next question depends on this re-read
  // to unlock; swallowing its own refusal left that question locked with
  // nothing on screen saying a reload would fix it.
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

/**
 * The state of every question at a glance, which is the whole reason the
 * design's own card carries a tag beside the points: a participant halfway
 * through an olympiad should not have to open four cards to find the one they
 * are on.
 */
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

  // "Accepted" and "attempts spent" both close a question and mean opposite
  // things. Collapsing them would tell a participant they solved something
  // they did not.
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

  // §6.1.1 puts sequential order on the server; this only names the question
  // the server is waiting on, so "after 2" beats "not yet".
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

  // Nothing is being worked on when nothing can be answered — after the
  // deadline, or before the contest opens, the server marks every question
  // unanswerable while leaving them open. Marking one "current" then would
  // point a participant at work they cannot do.
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
    // And the row is not marked either. The tag alone would have hidden this:
    // a locked question already reads "after N", so only the mark on the row
    // itself says which question the participant is on.
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

// docs/ARCHITECTURE.md §6.1.1: place is decided
// by how many questions are solved and, at a tie, by penalty time — a
// question's own points are never shown to a participant in this mode, and a
// correct answer is worth mentioning without a point value attached.
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
