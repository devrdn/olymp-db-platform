import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { loggedQuery } from "@/components/product/test-fixtures";
import type { Answers, Attempt } from "@/lib/api/journal";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { MyAnswers } from "./my-answers";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const t = () => dict.profile.report.answers;

function attempt(no: number, overrides: Partial<Attempt> = {}): Attempt {
  return {
    id: `s${no}`,
    questionOrd: 1,
    attemptNo: no,
    value: `value ${no}`,
    correct: false,
    points: 0,
    submittedAt: "2026-05-14T08:20:00.000Z",
    queries: [],
    moreQueries: 0,
    ...overrides,
  };
}

const answers: Answers = {
  truncated: false,
  questions: [
    {
      questionId: "q-1",
      questionOrd: 1,
      attempts: [attempt(1, { queries: [loggedQuery(3), loggedQuery(4)], moreQueries: 7 }), attempt(2, { correct: true, points: 20 })],
    },
  ],
};

function renderTab(value: Answers = answers) {
  return render(
    <MyAnswers
      answers={value}
      t={dict.profile.report}
      statuses={dict.participant.play.workspace.log.status}
      locale="en"
    />,
  );
}

describe("my answers", () => {
  test("groups the attempts by question, with the verdict and the points", () => {
    renderTab();

    expect(screen.getByRole("heading", { name: t().question.replace("{n}", "1") })).toBeInTheDocument();
    const rows = screen.getAllByRole("listitem");
    expect(within(rows[0]).getByText(t().wrong)).toBeInTheDocument();
    expect(within(rows[1]).getByText(t().correct)).toBeInTheDocument();
    expect(within(rows[1]).getByText(t().points.replace("{n}", "20"))).toBeInTheDocument();
  });

  test("opens an attempt to the queries that led to it, and counts the ones left out", () => {
    renderTab();

    // Two carried and seven counted: the toggle names all nine.
    fireEvent.click(screen.getByRole("button", { name: t().queriesToggle.replace("{n}", "9") }));
    expect(screen.getByText("SELECT 3")).toBeInTheDocument();
    expect(screen.getByText(t().moreQueries.replace("{n}", "7"))).toBeInTheDocument();
  });

  test("shows no address on the queries under an attempt", () => {
    renderTab();

    fireEvent.click(screen.getByRole("button", { name: t().queriesToggle.replace("{n}", "9") }));
    expect(screen.queryByText("10.0.0.1")).not.toBeInTheDocument();
  });

  test("says when an attempt had no queries before it", () => {
    renderTab();

    fireEvent.click(screen.getByRole("button", { name: t().queriesToggle.replace("{n}", "0") }));
    expect(screen.getByText(t().noQueries)).toBeInTheDocument();
  });

  test("says when nothing was answered at all", () => {
    renderTab({ truncated: false, questions: [] });
    expect(screen.getByText(t().empty)).toBeInTheDocument();
  });
});
