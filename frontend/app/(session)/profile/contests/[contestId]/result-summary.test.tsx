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

/** A complete standing to override, since `report().result` may be null. */
function result(overrides: Partial<NonNullable<ProfileReport["result"]>> = {}): NonNullable<ProfileReport["result"]> {
  return {
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
    ...overrides,
  };
}

function report(overrides: Partial<ProfileReport> = {}): ProfileReport {
  return {
    contestId: CONTEST,
    title: "The Greenhouse",
    status: "finished",
    startsAt: "2026-05-14T07:00:00Z",
    endsAt: "2026-05-14T10:00:00Z",
    result: result(),
    startedAt: "2026-05-14T07:02:00Z",
    queries: 120,
    successfulQueries: 98,
    workedMs: 5_400_000,
    disqualified: false,
    questions: [
      { questionId: `${CONTEST}-1`, ord: 1, attempts: 2, solved: true, solvedAt: "2026-05-14T08:00:00Z", points: 20, penalty: 0 },
      { questionId: `${CONTEST}-2`, ord: 2, attempts: 3, solved: false, solvedAt: undefined, points: 0, penalty: 0 },
    ],
    truncated: false,
    ...overrides,
  };
}

/** A strip figure by caption, scoped to the strip: "Points" is also a table heading. */
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
    // 5,400,000 ms is an hour and a half.
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
    // 08:00 UTC in May is 11:00 in the contest's zone.
    expect(within(rows[0]).getByText("11:00")).toBeInTheDocument();
    expect(within(rows[1]).getByText(t().questions.unsolved)).toBeInTheDocument();
    expect(within(rows[1]).getAllByText(t().questions.never).length).toBeGreaterThan(0);
  });
});

/** ICPC has no points (the server sends 0); solved and penalty are the result. */
describe("a contest scored the ICPC way", () => {
  test("shows solved and penalty, and no points anywhere", () => {
    renderSummary(
      report({
        result: result({ scoring: "icpc", points: 0, solved: 4, penalty: 87 }),
        questions: [
          // No points in this mode; the minutes are the cost.
          { questionId: `${CONTEST}-1`, ord: 1, attempts: 2, solved: true, solvedAt: "2026-05-14T08:00:00Z", points: 0, penalty: 47 },
          { questionId: `${CONTEST}-2`, ord: 2, attempts: 3, solved: false, solvedAt: undefined, points: 0, penalty: 0 },
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

  /** Under ICPC the column prints minutes, not the zero points. */
  test("prints the minutes a question cost, not the points it did not earn", () => {
    renderSummary(
      report({
        result: result({ scoring: "icpc", points: 0, solved: 1, penalty: 47 }),
        questions: [
          { questionId: `${CONTEST}-1`, ord: 1, attempts: 2, solved: true, solvedAt: "2026-05-14T08:00:00Z", points: 0, penalty: 47 },
        ],
      }),
    );

    const table = screen.getByRole("table", { name: t().questions.heading });
    const row = within(table).getAllByRole("row")[1];
    expect(within(row).getByText("47")).toBeInTheDocument();
  });
});

/** The truncation note counts attempts, not questions. */
describe("a report cut at the bound", () => {
  test("counts the attempts it carries, not the questions", () => {
    renderSummary(report({ truncated: true }));

    const questions = report().questions;
    const attempts = questions.reduce((sum, question) => sum + question.attempts, 0);
    expect(attempts).toBe(5);
    expect(screen.getByText(t().questions.truncated.replace("{n}", "5"))).toBeInTheDocument();
    expect(screen.queryByText(t().questions.truncated.replace("{n}", "2"))).not.toBeInTheDocument();
  });

  test("says nothing when the report is whole", () => {
    renderSummary();
    expect(screen.queryByText(/counted here/i)).not.toBeInTheDocument();
  });
});

describe("a table that is not open", () => {
  /** No open table, no place. */
  test("says where the place will appear rather than leaving a gap", () => {
    renderSummary(
      report({ result: result({ state: "frozen", placeOpen: false, place: null, participants: null }) }),
    );

    expect(screen.getByText(t().result.placePending)).toBeInTheDocument();
    expect(within(strip()).queryByText(t().result.place)).not.toBeInTheDocument();
  });

  /** A contest that never opened must not promise a reveal. */
  test("says the contest never opened rather than promising a reveal", () => {
    renderSummary(
      report({
        status: "published",
        disqualified: true,
        result: result({
          points: 0,
          solved: 0,
          state: "not_started",
          placeOpen: false,
          place: null,
          participants: null,
        }),
      }),
    );

    expect(screen.getByText(t().result.placeNotStarted)).toBeInTheDocument();
    expect(screen.queryByText(t().result.placePending)).not.toBeInTheDocument();
  });
});

/**
 * The published table is bounded, so someone below the cut has no row; the
 * server sends no result, and the screen says the standing is missing, not
 * their work.
 */
describe("a row the published table does not carry", () => {
  test("says the row is outside the table and still shows the session", () => {
    renderSummary(report({ result: null }));

    expect(screen.getByText(t().result.outsideTable)).toBeInTheDocument();
    expect(figure(t().result.queries)).toBe("120");
    expect(figure(t().result.successful)).toBe("98");
    expect(within(strip()).queryByText(t().result.points)).not.toBeInTheDocument();
    expect(within(strip()).queryByText(t().result.place)).not.toBeInTheDocument();
    expect(screen.queryByText(t().result.placePending)).not.toBeInTheDocument();
  });

  test("still names every question the participant answered", () => {
    renderSummary(report({ result: null }));

    const table = screen.getByRole("table", { name: t().questions.heading });
    expect(within(table).getAllByRole("row").slice(1)).toHaveLength(2);
  });
});

describe("a contest with one winner", () => {
  test("says the winner won", () => {
    renderSummary(
      report({
        result: result({ scoring: "winner", points: 0, place: 1, participants: 12, winner: true }),
      }),
    );

    expect(screen.getByText(t().result.winner)).toBeInTheDocument();
    expect(figure(t().result.place)).toContain("1");
  });

  /** Winner mode places only the winner; everyone else gets their own sentence. */
  test("tells everybody else that this contest places only the winner", () => {
    renderSummary(
      report({
        result: result({ scoring: "winner", points: 0, place: null, participants: null, winner: false }),
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
