import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Answers, Attempt } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { AnswersTab } from "./answers-tab";
import { loggedQuery } from "./test-fixtures";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

function attempt(no: number, overrides: Partial<Attempt> = {}): Attempt {
  return {
    id: `s${no}`,
    questionOrd: 1,
    attemptNo: no,
    value: `value ${no}`,
    correct: false,
    points: 0,
    submittedAt: "2026-09-20T10:20:00.000Z",
    queries: [],
    moreQueries: 0,
    ...overrides,
  };
}

const answers: Answers = {
  truncated: false,
  questions: [
    {
      questionId: "q1",
      questionOrd: 1,
      attempts: [
        attempt(1, { queries: [loggedQuery(1, { sql: "SELECT a" }), loggedQuery(2, { sql: "SELECT b" })], moreQueries: 3 }),
        attempt(2, { value: "42", correct: true, points: 5 }),
      ],
    },
    { questionId: "q2", questionOrd: 2, attempts: [attempt(1, { questionOrd: 2, value: "Smith" })] },
  ],
};

const t = () => dict.workspace.monitor.participant.answers;

function renderTab(value: Answers = answers) {
  return render(<AnswersTab answers={value} dict={dict} locale="en" />);
}

describe("the answers tab", () => {
  test("lists each question's attempts in order, with value, correctness and points", () => {
    renderTab();
    const first = screen.getByRole("region", { name: "Question 1" });
    const attempts = within(first).getAllByRole("listitem");
    expect(attempts).toHaveLength(2);
    expect(within(attempts[0]).getByText("Attempt 1")).toBeInTheDocument();
    expect(within(attempts[0]).getByText("value 1")).toBeInTheDocument();
    expect(within(attempts[0]).getByText("wrong")).toBeInTheDocument();
    expect(within(attempts[1]).getByText("42")).toBeInTheDocument();
    expect(within(attempts[1]).getByText("correct")).toBeInTheDocument();
    expect(within(attempts[1]).getByText("5 pts")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Question 2" })).toHaveTextContent("Smith");
  });

  test("an attempt opens to the queries that led to it, and counts those not shown", () => {
    renderTab();
    const toggle = screen.getByRole("button", { name: "Queries that led to it: 5" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("SELECT a")).not.toBeInTheDocument();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("SELECT a")).toBeInTheDocument();
    expect(screen.getByText("SELECT b")).toBeInTheDocument();
    expect(screen.getByText(t().moreQueries.replace("{n}", "3"))).toBeInTheDocument();
  });

  test("an attempt with no query before it says so", () => {
    renderTab();
    fireEvent.click(screen.getAllByRole("button", { name: "Queries that led to it: 0" })[0]);
    expect(screen.getByText(t().noQueries)).toBeInTheDocument();
  });

  test("says when there are none, and when only the first attempts are shown", () => {
    renderTab({ truncated: false, questions: [] });
    expect(screen.getByText(t().empty)).toBeInTheDocument();

    renderTab({ ...answers, truncated: true });
    expect(screen.getByText(t().truncated.replace("{n}", "3"))).toBeInTheDocument();
  });
});
