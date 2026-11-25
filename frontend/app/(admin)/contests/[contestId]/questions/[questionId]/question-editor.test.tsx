import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { Question } from "@/lib/api/content";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

// The real Server Action would pull in Next's server runtime; `vi.hoisted`
// because `vi.mock` factories run first.
const { saveQuestionAction } = vi.hoisted(() => ({
  saveQuestionAction: vi.fn(async (previous: unknown, form: FormData) => {
    void previous;
    void form;
    return { ok: true };
  }),
}));
vi.mock("./actions", () => ({ saveQuestionAction }));
const { refresh } = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh }),
}));

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
        contest={{ id: "c1", version: 3 }}
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
        dict={dict}
      />,
    );

    const penalty = screen.getByLabelText(dict.workspace.question.shape.penalty);
    expect(penalty).toHaveValue(20);
    expect(
      screen.getByText(fill(dict.workspace.question.shape.penaltyPreview, { n: "2", points: "10" })),
    ).toBeInTheDocument();

    await user.clear(penalty);
    await user.type(penalty, "50");

    expect(
      screen.getByText(fill(dict.workspace.question.shape.penaltyPreview, { n: "5", points: "10" })),
    ).toBeInTheDocument();
  });

  test("warns about the sequential publish-gate refusal only while attempts are unlimited", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contest={{ id: "c1", version: 3 }}
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="icpc"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ kind: "final", maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="winner"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ kind: "final", maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
        dict={dict}
      />,
    );

    expect(screen.queryByText(dict.workspace.question.shape.winnerFinalNeedsAttempts)).toBeNull();
  });

  test("says nothing about sequential order when this contest does not use it", () => {
    render(
      <QuestionEditor
        contest={{ id: "c1", version: 3 }}
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ kind: "choice", choiceIds: ["a", "b"] })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
        contest={{ id: "c1", version: 3 }}
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
        scoring={scoring}
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
    expect(screen.queryByText(fill(t.penaltyPreview, { n: "2", points: "10" }))).toBeNull();
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
    expect(screen.queryByText(fill(t.penaltyPreview, { n: "2", points: "10" }))).toBeNull();
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
    expect(screen.getByText(fill(t.penaltyPreview, { n: "2", points: "10" }))).toBeInTheDocument();
  });
});

// A bonus question (docs/ARCHITECTURE.md §6.1.1) gives back penalty points and
// is charged none itself; points scoring only.
describe("QuestionEditor, the bonus question", () => {
  function renderBonus(overrides: Partial<Question> = {}, scoring: "points" | "winner" | "icpc" = "points", editable = true) {
    render(
      <QuestionEditor
        contest={{ id: "c1", version: 3 }}
        question={question({ points: 4, penaltyPct: 25, ...overrides })}
        languages={["en"]}
        editable={editable}
        scoring={scoring}
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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
    expect(screen.queryByText(fill(t.penaltyPreview, { n: "1", points: "4" }))).toBeNull();
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
        contest={{ id: "c1", version: 3 }}
        question={question({ answers: [{ id: "a1", matchKind: "regex", value: "(?i)john\\s+smith" }] })}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
        dict={dict}
      />,
    );

    expect(screen.getByText(dict.workspace.question.answers.regexHint)).toBeVisible();
  });
});

// A title per language, bounded as the API bounds it (contests.QuestionText).
describe("QuestionEditor, the title", () => {
  const t = () => dict.workspace.question.texts;
  const counter = (n: number) => fill(t().titleCounter, { n, max: "200" });

  function renderTitled(editable = true) {
    render(
      <QuestionEditor
        contest={{ id: "c1", version: 3 }}
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
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
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

describe("QuestionEditor, hiding the deciding question", () => {
  function renderHiding(
    overrides: Partial<Question>,
    counts: { otherVisibleQuestions?: number; otherVisibleFinals?: number },
    scoring: "points" | "winner" | "icpc" = "points",
    editable = true,
  ) {
    render(
      <QuestionEditor
        contest={{ id: "c1", version: 3 }}
        question={question(overrides)}
        languages={["en"]}
        editable={editable}
        scoring={scoring}
        sequentialActive={false}
        otherVisibleQuestions={counts.otherVisibleQuestions}
        otherVisibleFinals={counts.otherVisibleFinals}
        dict={dict}
      />,
    );
    return screen.getByRole("checkbox", { name: dict.workspace.question.shape.visible });
  }

  const t = () => dict.workspace.question.shape;

  test("warns that hiding the only shown question blocks publishing", async () => {
    const checkbox = renderHiding({}, { otherVisibleQuestions: 0 });
    expect(screen.queryByText(t().hiddenLastVisible)).not.toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(checkbox);
    const warning = screen.getByText(t().hiddenLastVisible);
    expect(checkbox).toHaveAttribute("aria-describedby", warning.id);
    expect(screen.getByRole("status")).toBe(warning);

    await user.click(checkbox);
    expect(screen.queryByText(t().hiddenLastVisible)).not.toBeInTheDocument();
  });

  test("warns that hiding the last shown final question blocks publishing under first to solve", async () => {
    const checkbox = renderHiding(
      { kind: "final", maxAttempts: 3 },
      { otherVisibleQuestions: 2, otherVisibleFinals: 0 },
      "winner",
    );
    await userEvent.setup().click(checkbox);

    expect(screen.getByText(t().hiddenWinnerFinal)).toBeInTheDocument();
    expect(screen.queryByText(t().hiddenLastVisible)).not.toBeInTheDocument();
  });

  test("says nothing while another final question is shown", async () => {
    const checkbox = renderHiding(
      { kind: "final", maxAttempts: 3 },
      { otherVisibleQuestions: 2, otherVisibleFinals: 1 },
      "winner",
    );
    await userEvent.setup().click(checkbox);

    expect(screen.queryByText(t().hiddenWinnerFinal)).not.toBeInTheDocument();
    expect(screen.queryByText(t().hiddenLastVisible)).not.toBeInTheDocument();
  });

  test("says nothing about a hidden final outside first to solve", async () => {
    const checkbox = renderHiding(
      { kind: "final" },
      { otherVisibleQuestions: 1, otherVisibleFinals: 0 },
      "points",
    );
    await userEvent.setup().click(checkbox);

    expect(screen.queryByText(t().hiddenWinnerFinal)).not.toBeInTheDocument();
    expect(screen.queryByText(t().hiddenLastVisible)).not.toBeInTheDocument();
  });

  test("warns at once about a question already stored hidden", () => {
    renderHiding({ isVisible: false }, { otherVisibleQuestions: 0 });
    expect(screen.getByText(t().hiddenLastVisible)).toBeInTheDocument();
  });

  test("says nothing about a non-final question in a winner contest", async () => {
    const checkbox = renderHiding(
      { kind: "text" },
      { otherVisibleQuestions: 2, otherVisibleFinals: 0 },
      "winner",
    );
    await userEvent.setup().click(checkbox);

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  test("says nothing when the page could not count the other questions", async () => {
    const checkbox = renderHiding({ kind: "final" }, {}, "winner");
    await userEvent.setup().click(checkbox);

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  test("says nothing in a locked editor", () => {
    renderHiding({ isVisible: false }, { otherVisibleQuestions: 0 }, "points", false);

    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});

describe("QuestionEditor, the version it was rendered at", () => {
  function editorAt(version: number) {
    return (
      <QuestionEditor
        contest={{ id: "c1", version }}
        question={question()}
        languages={["en"]}
        editable
        scoring="points"
        sequentialActive={false}
        otherVisibleQuestions={1}
        otherVisibleFinals={1}
        dict={dict}
      />
    );
  }

  test("a save over a contest that moved since says so, and the reload starts the form over", async () => {
    saveQuestionAction.mockResolvedValueOnce({ code: "contest_changed" } as never);
    refresh.mockReset();
    render(editorAt(3));

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.question.save }));
    expect(await screen.findByRole("alert")).toHaveTextContent(dict.errors.contest_changed);
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.reload }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The save re-reads the page, whose contest now carries the moved version.
  test("a save sends the version it was rendered at, and the next one the version the page re-read", async () => {
    const view = render(editorAt(3));

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.question.save }));
    expect(await screen.findByRole("status")).toHaveTextContent(dict.workspace.question.saved);
    view.rerender(editorAt(4));
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.question.save }));

    await vi.waitFor(() => expect(saveQuestionAction).toHaveBeenCalledTimes(2));
    expect(saveQuestionAction.mock.calls.map(([, form]) => form.get("expectedVersion"))).toEqual(["3", "4"]);
  });
});

// The reload re-reads the page and starts the editor over: the kind held in
// state is the colleague's afterwards, not the refused one.
test("QuestionEditor, a reload after a refused save shows, and sends, the colleague's kind", async () => {
  saveQuestionAction.mockClear();
  saveQuestionAction.mockResolvedValueOnce({ code: "contest_changed" } as never);
  const user = userEvent.setup();
  const editorFor = (q: Question, version: number) => (
    <QuestionEditor
      contest={{ id: "c1", version }}
      question={q}
      languages={["en"]}
      editable
      scoring="points"
      sequentialActive={false}
      otherVisibleQuestions={1}
      otherVisibleFinals={1}
      dict={dict}
    />
  );
  const kind = (value: Question["kind"]) =>
    screen.getByRole("radio", { name: new RegExp(dict.workspace.questions.kind[value]) });
  const view = render(editorFor(question({ kind: "text" }), 3));

  await user.click(kind("final"));
  await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));
  await user.click(await screen.findByRole("button", { name: dict.workspace.reload }));
  view.rerender(editorFor(question({ kind: "choice", choiceIds: ["a", "b"] }), 4));

  expect(kind("choice")).toBeChecked();
  await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));
  await vi.waitFor(() => expect(saveQuestionAction).toHaveBeenCalledTimes(2));
  const form = saveQuestionAction.mock.calls[1][1];
  expect(form.get("expectedVersion")).toBe("4");
  expect(form.get("kind")).toBe("choice");
});

// A newer saved question reaching an edited editor never replaces the draft:
// the save is refused, and the reload takes the colleague's question.
test("QuestionEditor, an edited draft keeps its kind over a newer question, is refused, and takes the question after the reload", async () => {
  saveQuestionAction.mockClear();
  saveQuestionAction.mockResolvedValueOnce({ code: "contest_changed" } as never);
  const user = userEvent.setup();
  const editorFor = (q: Question, version: number) => (
    <QuestionEditor
      contest={{ id: "c1", version }}
      question={q}
      languages={["en"]}
      editable
      scoring="points"
      sequentialActive={false}
      otherVisibleQuestions={1}
      otherVisibleFinals={1}
      dict={dict}
    />
  );
  const kind = (value: Question["kind"]) =>
    screen.getByRole("radio", { name: new RegExp(dict.workspace.questions.kind[value]) });
  const view = render(editorFor(question({ kind: "text" }), 3));

  await user.click(kind("final"));
  view.rerender(editorFor(question({ kind: "choice", choiceIds: ["a", "b"] }), 4));
  expect(kind("final")).toBeChecked();
  await user.click(screen.getByRole("button", { name: dict.workspace.question.save }));
  await vi.waitFor(() => expect(saveQuestionAction).toHaveBeenCalledTimes(1));
  expect(saveQuestionAction.mock.calls[0][1].get("expectedVersion")).toBe("3");
  await user.click(await screen.findByRole("button", { name: dict.workspace.reload }));

  expect(kind("choice")).toBeChecked();
});
