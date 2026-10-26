import { render, screen, within } from "@testing-library/react";
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
    expect(screen.queryByText(dict.workspace.question.shape.sequentialIcpcUnlimited)).toBeNull();

    await user.type(screen.getByLabelText(dict.workspace.question.shape.attempts), "3");

    expect(
      screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts),
    ).toBeNull();
  });

  test("says an unlimited question is allowed under ICPC, and that publishing is not blocked", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts)).toBeNull();
    expect(screen.getByText(dict.workspace.question.shape.sequentialIcpcUnlimited)).toBeInTheDocument();

    await user.type(screen.getByLabelText(dict.workspace.question.shape.attempts), "3");

    expect(screen.queryByText(dict.workspace.question.shape.sequentialIcpcUnlimited)).toBeNull();
  });

  test("says nothing about unlimited attempts under ICPC when the order is free", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.question.shape.sequentialIcpcUnlimited)).toBeNull();
    expect(screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts)).toBeNull();
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

// A mode shows only the fields it uses: ICPC has no per-question points or
// penalty, first-to-solve has no penalty. The stored values stay in the form so
// that switching the mode back before the start restores them.
describe("QuestionEditor, fields the scoring mode does not use", () => {
  function renderScored(scoring: "points" | "winner" | "icpc") {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring={scoring}
        sequentialActive={false}
        dict={dict}
      />,
    );
    return dict.workspace.question.shape;
  }

  const savedForm = async () => {
    await userEvent.setup().click(screen.getByRole("button", { name: dict.workspace.question.save }));
    expect(saveQuestionAction).toHaveBeenCalled();
    return saveQuestionAction.mock.calls[0][1] as FormData;
  };

  test("under ICPC shows neither the points, the penalty nor the penalty preview", () => {
    const t = renderScored("icpc");

    expect(screen.queryByLabelText(t.points)).toBeNull();
    expect(screen.queryByLabelText(t.penalty)).toBeNull();
    expect(screen.queryByText(t.penaltyPreview.replace("{n}", "2").replace("{points}", "10"))).toBeNull();
    expect(screen.getByLabelText(t.attempts)).toBeInTheDocument();
  });

  test("under ICPC a save still carries the stored points and penalty", async () => {
    renderScored("icpc");

    const form = await savedForm();
    expect(form.get("points")).toBe("10");
    expect(form.get("penaltyPct")).toBe("20");
  });

  test("under first to solve shows the points but not the penalty or its preview", () => {
    const t = renderScored("winner");

    expect(screen.getByLabelText(t.points)).toHaveValue(10);
    expect(screen.queryByLabelText(t.penalty)).toBeNull();
    expect(screen.queryByText(t.penaltyPreview.replace("{n}", "2").replace("{points}", "10"))).toBeNull();
  });

  test("under first to solve a save carries the edited points and the stored penalty", async () => {
    const user = userEvent.setup();
    const t = renderScored("winner");

    await user.clear(screen.getByLabelText(t.points));
    await user.type(screen.getByLabelText(t.points), "7");
    await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));

    const form = saveQuestionAction.mock.calls[0][1] as FormData;
    expect(form.getAll("points")).toEqual(["7"]);
    expect(form.getAll("penaltyPct")).toEqual(["20"]);
  });

  test("under points shows the points, the penalty and its preview", () => {
    const t = renderScored("points");

    expect(screen.getByLabelText(t.points)).toBeEnabled();
    expect(screen.getByLabelText(t.penalty)).toBeEnabled();
    expect(screen.getByText(t.penaltyPreview.replace("{n}", "2").replace("{points}", "10"))).toBeInTheDocument();
  });
});

// A bonus question (docs/ARCHITECTURE.md §6.1.1) gives back penalty points and
// is charged none itself; points scoring only.
describe("QuestionEditor, the bonus question", () => {
  function renderBonus(overrides: Partial<Question> = {}, scoring: "points" | "winner" | "icpc" = "points", editable = true) {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 4, penaltyPct: 25, ...overrides })}
        languages={["en"]}
        editable={editable}
        scoring={scoring}
        sequentialActive={false}
        dict={dict}
      />,
    );
    return screen.getByRole("checkbox", { name: dict.workspace.question.shape.bonus });
  }

  test("offers a checkbox, unticked for a regular question, with the rule spelled out beside it", () => {
    const checkbox = renderBonus();

    expect(checkbox).not.toBeChecked();
    expect(checkbox).toBeEnabled();
    expect(checkbox).toHaveAccessibleDescription(dict.workspace.question.shape.bonusHint);
    expect(screen.getByText(dict.workspace.question.shape.bonusHint)).toBeVisible();
  });

  test("shows a stored bonus ticked, with the penalty field disabled and why", () => {
    const checkbox = renderBonus({ isBonus: true });

    const t = dict.workspace.question.shape;
    expect(checkbox).toBeChecked();
    expect(screen.getByLabelText(t.penalty)).toBeDisabled();
    expect(screen.getByText(t.bonusNoPenalty)).toBeInTheDocument();
    expect(screen.queryByText(t.penaltyPreview.replace("{n}", "1").replace("{points}", "4"))).toBeNull();
  });

  test("ticking it from the keyboard sets the penalty aside, and unticking brings it back", async () => {
    const user = userEvent.setup();
    const checkbox = renderBonus();
    const penalty = screen.getByLabelText(dict.workspace.question.shape.penalty);

    checkbox.focus();
    await user.keyboard(" ");
    expect(checkbox).toBeChecked();
    expect(penalty).toBeDisabled();

    await user.keyboard(" ");
    expect(checkbox).not.toBeChecked();
    expect(penalty).toBeEnabled();
  });

  // The percentage is kept, unapplied, so unticking the bonus later restores it.
  test("submits the flag and still the stored penalty on save", async () => {
    const user = userEvent.setup();
    renderBonus({ isBonus: true });

    await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));

    const form = saveQuestionAction.mock.calls[0][1] as FormData;
    expect(form.get("isBonus")).toBe("on");
    expect(form.get("penaltyPct")).toBe("25");
  });

  test.each(["winner", "icpc"] as const)("is disabled under %s scoring, saying only points scoring has one", (scoring) => {
    const checkbox = renderBonus({}, scoring);

    expect(checkbox).toBeDisabled();
    expect(checkbox).not.toBeChecked();
    expect(screen.getByText(dict.workspace.question.shape.bonusPointsOnly)).toBeInTheDocument();
  });

  test("is read-only when the question cannot be edited", () => {
    expect(renderBonus({ isBonus: true }, "points", false)).toBeDisabled();
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

// A title per language, bounded as the API bounds it (contests.QuestionText).
describe("QuestionEditor, the title", () => {
  const t = () => dict.workspace.question.texts;
  const counter = (n: number) => t().titleCounter.replace("{n}", String(n)).replace("{max}", "200");

  function renderTitled(editable = true) {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({
          texts: {
            en: { title: "The culprit", bodyMd: "Who did it?", choices: {} },
            ro: { title: "", bodyMd: "Cine a făcut-o?", choices: {} },
          },
        })}
        languages={["en", "ro"]}
        editable={editable}
        scoring="points"
        sequentialActive={false}
        dict={dict}
      />,
    );
  }

  test("offers a title field in each language, beside that language's question", () => {
    renderTitled();

    const english = within(screen.getByRole("group", { name: "en" }));
    const romanian = within(screen.getByRole("group", { name: "ro" }));
    expect(english.getByLabelText(t().title)).toHaveValue("The culprit");
    expect(english.getByLabelText(t().body)).toHaveValue("Who did it?");
    expect(romanian.getByLabelText(t().title)).toHaveValue("");
  });

  test("stops at the bound and counts towards it as the author types", async () => {
    const user = userEvent.setup();
    renderTitled();
    const english = within(screen.getByRole("group", { name: "en" }));
    const title = english.getByLabelText(t().title);

    expect(title).toHaveAttribute("maxLength", "200");
    expect(english.getByText(counter(11))).toBeInTheDocument();
    expect(title).toHaveAccessibleDescription(expect.stringContaining(counter(11)));

    await user.type(title, "!");

    expect(english.getByText(counter(12))).toBeInTheDocument();
  });

  test("says the title is optional and what a participant sees", () => {
    renderTitled();

    const english = within(screen.getByRole("group", { name: "en" }));
    expect(english.getByLabelText(t().title)).toHaveAccessibleDescription(expect.stringContaining(t().titleHint));
  });

  test("submits each language's title under its own name", async () => {
    const user = userEvent.setup();
    renderTitled();
    const romanian = within(screen.getByRole("group", { name: "ro" }));

    await user.type(romanian.getByLabelText(t().title), "Vinovatul");
    await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));

    const form = saveQuestionAction.mock.calls[0][1] as FormData;
    expect(form.get("title.en")).toBe("The culprit");
    expect(form.get("title.ro")).toBe("Vinovatul");
    expect(form.get("body.ro")).toBe("Cine a făcut-o?");
  });

  test("is read-only when the question cannot be edited", () => {
    renderTitled(false);

    const english = within(screen.getByRole("group", { name: "en" }));
    expect(english.getByLabelText(t().title)).toBeDisabled();
  });
});
