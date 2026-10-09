import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Question } from "@/lib/api/content";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveQuestionAction } = vi.hoisted(() => ({
  saveQuestionAction: vi.fn(async (previous: unknown, form: FormData) => {
    void previous;
    void form;
    return { saved: true };
  }),
}));
vi.mock("./actions", () => ({ saveQuestionAction }));

import { QuestionEditor } from "./question-editor";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  saveQuestionAction.mockClear();
});

function question(overrides: Partial<Question> = {}): Question {
  return {
    id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    ord: 1,
    kind: "text",
    points: 10,
    maxAttempts: undefined,
    penaltyPct: 0,
    isVisible: true,
    choiceIds: [],
    texts: {},
    answers: [],
    ...overrides,
  } as Question;
}

// The penalty is editable here with its effect worked out, and the
// sequential-progression publish refusal is stated.
describe("QuestionEditor, the penalty and the sequential warning", () => {
  test("shows what a wrong attempt costs on this question, and updates it live", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );

    const penalty = screen.getByLabelText(dict.workspace.question.shape.penalty);
    expect(penalty).toHaveValue(20);
    expect(
      screen.getByText(dict.workspace.question.shape.penaltyPreview.replace("{n}", "2").replace("{points}", "10")),
    ).toBeInTheDocument();

    await user.clear(penalty);
    await user.type(penalty, "50");

    expect(
      screen.getByText(dict.workspace.question.shape.penaltyPreview.replace("{n}", "5").replace("{points}", "10")),
    ).toBeInTheDocument();
  });

  test("warns about the sequential publish-gate refusal only while attempts are unlimited", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive
        dict={dict}
      />,
    );

    expect(
      screen.getByText(dict.workspace.question.shape.sequentialNeedsAttempts),
    ).toBeInTheDocument();

    await user.type(screen.getByLabelText(dict.workspace.question.shape.attempts), "3");

    expect(
      screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts),
    ).toBeNull();
  });

  test("warns about the winner-mode publish-gate refusal on an unlimited final question", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ kind: "final", maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="winner"
        sequentialActive={false}
        dict={dict}
      />,
    );

    const warning = dict.workspace.question.shape.winnerFinalNeedsAttempts;
    expect(screen.getByText(warning)).toBeInTheDocument();

    await user.type(screen.getByLabelText(dict.workspace.question.shape.attempts), "3");
    expect(screen.queryByText(warning)).toBeNull();
  });

  test("says nothing about a final question's attempts outside winner mode", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ kind: "final", maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.question.shape.winnerFinalNeedsAttempts)).toBeNull();
  });

  test("says nothing about sequential order when this contest does not use it", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(
      screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts),
    ).toBeNull();
  });
});

// Rules stay under the field; explanations sit behind a "?".
describe("QuestionEditor, rules on screen and explanations behind a question mark", () => {
  function renderChoice() {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ kind: "choice", choiceIds: ["a", "b"] })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );
  }

  test("keeps the attempts rule and the option-identifier rule visible", () => {
    renderChoice();

    const t = dict.workspace.question.shape;
    expect(screen.getByText(t.attemptsHint)).toBeVisible();
    expect(screen.getByText(t.choicesHint)).toBeVisible();
    // The rule, then the explanation, since screen-reader users reach the
    // control, not the button.
    expect(screen.getByLabelText(t.choices)).toHaveAccessibleDescription(
      `${t.choicesHint} ${t.choicesHelp}`,
    );
  });

  test("puts the penalty's and the identifiers' reasons behind a question mark", async () => {
    const user = userEvent.setup();
    renderChoice();

    const t = dict.workspace.question.shape;
    expect(screen.getByText(t.penaltyHelp)).not.toBeVisible();
    expect(screen.getByText(t.choicesHelp)).not.toBeVisible();

    const penaltyHint = screen
      .getAllByRole("button", { name: dict.chrome.helpLabel })
      .find((button) => button.getAttribute("aria-describedby") === screen.getByText(t.penaltyHelp).id)!;
    await user.click(penaltyHint);

    expect(screen.getByText(t.penaltyHelp)).toBeVisible();
    // Beside the label, so the field is named by its label alone.
    expect(screen.getByRole("spinbutton", { name: t.penalty })).toBeInTheDocument();
  });

  test("keeps the checkbox named by its own words, with the reason beside it", () => {
    renderChoice();

    const t = dict.workspace.question.shape;
    expect(screen.getByRole("checkbox", { name: t.visible })).toBeInTheDocument();
    expect(screen.getByText(t.visibleHelp)).not.toBeVisible();
  });
});

// ICPC (docs/ARCHITECTURE.md §6.1.1) has no per-question points or penalty. The
// fields stay (the mode can be reverted) but are disabled, and a save must keep
// their values.
describe("QuestionEditor, ICPC scoring", () => {
  test("disables the points and penalty fields, with the reason beside them", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive={false}
        dict={dict}
      />,
    );

    const t = dict.workspace.question.shape;
    const points = screen.getByLabelText(t.points);
    const penalty = screen.getByLabelText(t.penalty);

    expect(points).toBeDisabled();
    expect(points).toHaveValue(10);
    expect(penalty).toBeDisabled();
    expect(penalty).toHaveValue(20);
    expect(screen.getAllByText(t.icpcDisabled).length).toBeGreaterThan(0);
  });

  test("says nothing about a wrong attempt's cost, since the penalty does not apply", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(
      screen.queryByText(dict.workspace.question.shape.penaltyPreview.replace("{n}", "2").replace("{points}", "10")),
    ).toBeNull();
  });

  // Disabled inputs are not submitted; a wording-only save must not zero the
  // points.
  test("still submits the disabled fields' current values on save", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive={false}
        dict={dict}
      />,
    );

    await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));

    expect(saveQuestionAction).toHaveBeenCalled();
    const form = saveQuestionAction.mock.calls[0][1] as FormData;
    expect(form.get("points")).toBe("10");
    expect(form.get("penaltyPct")).toBe("20");
  });
});

// Patterns match the whole answer, which authors expecting substrings get
// wrong; the rule is stated beside the answers.
describe("QuestionEditor, the reference answers", () => {
  test("says that a regular expression must match the whole answer", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ answers: [{ id: "a1", matchKind: "regex", value: "(?i)john\\s+smith" }] })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.workspace.question.answers.regexHint)).toBeVisible();
  });
});
