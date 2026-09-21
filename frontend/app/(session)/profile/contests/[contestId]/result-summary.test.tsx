import { render, screen, within } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { ProfileReport } from "@/lib/api/profile";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ResultSummary } from "./result-summary";

const CONTEST = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const t = () => dict.profile.report;

function report(overrides: Partial<ProfileReport> = {}): ProfileReport {
  return {
    contestId: CONTEST,
    title: "The Greenhouse",
    status: "finished",
    startsAt: "2026-05-14T07:00:00Z",
    endsAt: "2026-05-14T10:00:00Z",
    result: {
      scoring: "points",
      points: 60,
      solved: 3,
      penalty: undefined,
      state: "final",
      placeOpen: true,
      place: 4,
      participants: 31,
      winner: false,
      truncated: false,
    },
    startedAt: "2026-05-14T07:02:00Z",
    queries: 120,
    successfulQueries: 98,
    workedMs: 5_400_000,
    disqualified: false,
    questions: [
      { questionId: `${CONTEST}-1`, ord: 1, attempts: 2, solved: true, solvedAt: "2026-05-14T08:00:00Z", points: 20 },
      { questionId: `${CONTEST}-2`, ord: 2, attempts: 3, solved: false, solvedAt: undefined, points: 0 },
    ],
    truncated: false,
    ...overrides,
  };
}

/**
 * One figure of the result strip, by the caption under it. Scoped to the
 * strip: "Points" is a caption there and a column heading in the table
 * below, and they are two different things with one word.
 */
function strip(): HTMLElement {
  return document.querySelector("dl") as HTMLElement;
}

function figure(label: string): string {
  const term = within(strip()).getByText(label);
  return term.parentElement?.textContent?.replace(label, "").trim() ?? "";
}

function renderSummary(value: ProfileReport = report()) {
  return render(<ResultSummary report={value} t={t()} locale="en" />);
}

describe("a contest scored in points", () => {
  test("shows the points, the questions solved, the work behind them and the place", () => {
    renderSummary();

    expect(figure(t().result.points)).toBe("60");
    expect(figure(t().result.solved)).toBe("3");
    expect(figure(t().result.queries)).toBe("120");
    expect(figure(t().result.successful)).toBe("98");
    // 5,400,000 ms is an hour and a half, as a clock reads it.
    expect(figure(t().result.worked)).toBe("1:30:00");
    expect(figure(t().result.place)).toContain("4");
    expect(within(strip()).getByText(t().result.placeOf.replace("{n}", "31"))).toBeInTheDocument();
    expect(screen.queryByText(t().result.placePending)).not.toBeInTheDocument();
  });

  test("names every question, whether it was solved, the attempts and when it first was", () => {
    renderSummary();

    const table = screen.getByRole("table", { name: t().questions.heading });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText(t().questions.solved)).toBeInTheDocument();
    expect(within(rows[0]).getByText("2")).toBeInTheDocument();
    // 08:00 UTC in May is 11:00 where the contest is held.
    expect(within(rows[0]).getByText("11:00")).toBeInTheDocument();
    expect(within(rows[1]).getByText(t().questions.unsolved)).toBeInTheDocument();
    expect(within(rows[1]).getAllByText(t().questions.never).length).toBeGreaterThan(0);
  });
});

/**
 * ICPC writes no points at all — the server sends nought — so a screen that
 * printed them would report nought over four solved questions. Solved and
 * penalty are the result in that mode.
 */
describe("a contest scored the ICPC way", () => {
  test("shows solved and penalty, and no points anywhere", () => {
    renderSummary(
      report({
        result: { ...report().result, scoring: "icpc", points: 0, solved: 4, penalty: 87 },
        questions: [
          { questionId: `${CONTEST}-1`, ord: 1, attempts: 2, solved: true, solvedAt: "2026-05-14T08:00:00Z", points: 27 },
        ],
      }),
    );

    expect(figure(t().result.solved)).toBe("4");
    expect(figure(t().result.penalty)).toBe("87");
    expect(within(strip()).queryByText(t().result.points)).not.toBeInTheDocument();

    const table = screen.getByRole("table", { name: t().questions.heading });
    expect(within(table).getByText(t().questions.columns.penalty)).toBeInTheDocument();
    expect(within(table).queryByText(t().questions.columns.points)).not.toBeInTheDocument();
  });
});

describe("a table that is not open", () => {
  /** The freeze is not worked around: with no open table there is no place. */
  test("says where the place will appear rather than leaving a gap", () => {
    renderSummary(
      report({ result: { ...report().result, state: "frozen", placeOpen: false, place: null, participants: null } }),
    );

    expect(screen.getByText(t().result.placePending)).toBeInTheDocument();
    expect(within(strip()).queryByText(t().result.place)).not.toBeInTheDocument();
  });
});

describe("a contest with one winner", () => {
  test("says the winner won", () => {
    renderSummary(
      report({
        result: { ...report().result, scoring: "winner", points: 0, place: 1, participants: 12, winner: true },
      }),
    );

    expect(screen.getByText(t().result.winner)).toBeInTheDocument();
    expect(figure(t().result.place)).toContain("1");
  });

  /**
   * An open table that places nobody but its winner leaves everybody else's
   * place null — which is a different thing from a frozen table, and gets a
   * different sentence.
   */
  test("tells everybody else that this contest places only the winner", () => {
    renderSummary(
      report({
        result: { ...report().result, scoring: "winner", points: 0, place: null, participants: null, winner: false },
      }),
    );

    expect(screen.getByText(t().result.unplaced)).toBeInTheDocument();
    expect(screen.queryByText(t().result.placePending)).not.toBeInTheDocument();
    expect(screen.queryByText(t().result.winner)).not.toBeInTheDocument();
  });
});

describe("a participant who was disqualified", () => {
  test("says so, and says nothing about why", () => {
    renderSummary(report({ disqualified: true }));

    const said = screen.getByText(t().disqualified);
    expect(said).toBeInTheDocument();
    expect(said.textContent).not.toMatch(/reason|because/i);
  });

  test("and their own numbers are still there", () => {
    renderSummary(report({ disqualified: true }));
    expect(figure(t().result.points)).toBe("60");
  });
});

describe("a participant who never started", () => {
  test("says so instead of printing a stretch of time it cannot name", () => {
    renderSummary(report({ startedAt: undefined, workedMs: undefined }));

    expect(screen.getByText(t().result.notStarted)).toBeInTheDocument();
    expect(figure(t().result.worked)).toBe(t().result.noWorked);
  });
});
