import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test } from "vitest";

import type { Question } from "@/lib/api/content";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { QuestionEditor } from "./question-editor";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
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

// Finding 1: the question editor sent no penalty at all — questions.penalty_pct
// was reachable only by a hand-crafted API call. These prove the field is on
// the same screen as the question's other settings, that an organizer sees
// what it means for this question rather than a bare number, and that
// sequential progression's own publish-gate refusal is said here too.
describe("QuestionEditor, the penalty and the sequential warning", () => {
  test("shows what a wrong attempt costs on this question, and updates it live", async () => {
    const user = userEvent.setup();
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ points: 10, penaltyPct: 20 })}
        languages={["en"]}
        editable
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

  test("says nothing about sequential order when this contest does not use it", () => {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ maxAttempts: undefined })}
        languages={["en"]}
        editable
        sequentialActive={false}
        dict={dict}
      />,
    );

    expect(
      screen.queryByText(dict.workspace.question.shape.sequentialNeedsAttempts),
    ).toBeNull();
  });
});

// The client's split, on the busiest form in the constructor: what a person
// needs before the mistake stays under the field; why the field exists sits
// behind a "?" beside its label.
describe("QuestionEditor, rules on screen and explanations behind a question mark", () => {
  function renderChoice() {
    render(
      <QuestionEditor
        contestId="c1"
        question={question({ kind: "choice", choiceIds: ["a", "b"] })}
        languages={["en"]}
        editable
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
    // The rule, then the explanation behind the "?": a screen-reader user who
    // moves between fields reaches the control, never the button beside it.
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
    // Beside the label, not in it: the field is still named by its label alone.
    expect(screen.getByRole("spinbutton", { name: t.penalty })).toBeInTheDocument();
  });

  test("keeps the checkbox named by its own words, with the reason beside it", () => {
    renderChoice();

    const t = dict.workspace.question.shape;
    expect(screen.getByRole("checkbox", { name: t.visible })).toBeInTheDocument();
    expect(screen.getByText(t.visibleHelp)).not.toBeVisible();
  });
});
