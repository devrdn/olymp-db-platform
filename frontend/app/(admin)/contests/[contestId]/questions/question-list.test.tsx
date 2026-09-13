import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import type { Question } from "@/lib/api/content";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The list's actions are Server Actions ("use server"): importing the real
// module pulls Next's server runtime into a component test, the same reason
// `question-editor.test.tsx` fakes its own.
vi.mock("./actions", () => ({
  addQuestionAction: vi.fn(async () => ({})),
  deleteQuestionAction: vi.fn(async () => ({})),
  reorderQuestionsAction: vi.fn(async () => ({})),
}));

import { QuestionList } from "./question-list";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

function question(overrides: Partial<Question> = {}): Question {
  return {
    id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    ord: 1,
    kind: "text",
    points: 37,
    maxAttempts: undefined,
    penaltyPct: 0,
    isVisible: true,
    choiceIds: [],
    texts: { en: { bodyMd: "Who did it?", choices: {} } },
    answers: [],
    ...overrides,
  } as Question;
}

// docs/superpowers/specs/2026-09-13-icpc-scoring-design.md, decision 1: a
// question has no points in ICPC scoring, so the list shows no points column
// there, the same way the question editor disables the field.
describe("QuestionList, the points column", () => {
  test("shows each question's points in points scoring", () => {
    render(
      <QuestionList
        contestId="c1"
        questions={[question()]}
        languages={["en"]}
        editable={false}
        scoring="points"
        dict={dict}
      />,
    );

    const table = screen.getByRole("table");
    expect(within(table).getByRole("columnheader", { name: dict.workspace.questions.columns.points })).toBeInTheDocument();
    expect(within(table).getByText("37")).toBeInTheDocument();
  });

  test("has no points header and no points cell in ICPC scoring", () => {
    render(
      <QuestionList
        contestId="c1"
        questions={[question()]}
        languages={["en"]}
        editable={false}
        scoring="icpc"
        dict={dict}
      />,
    );

    const table = screen.getByRole("table");
    expect(
      within(table).queryByRole("columnheader", { name: dict.workspace.questions.columns.points }),
    ).not.toBeInTheDocument();
    expect(within(table).queryByText("37")).not.toBeInTheDocument();
    // The row keeps a cell under every remaining header.
    const headers = within(table).getAllByRole("columnheader").length;
    const cells = within(within(table).getAllByRole("row")[1]).getAllByRole("cell").length;
    expect(cells).toBe(headers);
  });
});
