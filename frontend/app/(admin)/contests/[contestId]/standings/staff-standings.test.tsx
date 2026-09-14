import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { StaffStandings } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchStaffStandingsAction } = vi.hoisted(() => ({
  fetchStaffStandingsAction: vi.fn(),
}));
vi.mock("./actions", () => ({
  revealStandingsAction: vi.fn(async () => ({ revealedAt: "2026-09-20T13:00:00Z" })),
  fetchStaffStandingsAction,
}));

import { STAFF_REFRESH_MS, StaffStandingsView } from "./staff-standings";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

function board(overrides: Partial<StaffStandings> = {}): StaffStandings {
  return {
    shown: { state: "frozen", frozenAt: "2026-09-20T11:30:00Z" },
    scoring: "points",
    freezeMin: 30,
    names: "login",
    revealedAt: undefined,
    generatedAt: "2026-09-20T12:00:00Z",
    truncated: false,
    questions: undefined,
    rows: [
      { place: 1, login: "holmes", fullName: "Sherlock Holmes", deleted: false, disqualified: false, points: 30, solved: 3, penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false },
      { place: 2, login: "moriarty", fullName: "James Moriarty", deleted: false, disqualified: true, points: 99, solved: 9, penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false },
    ],
    ...overrides,
  };
}

function show(value: StaffStandings, status: "running" | "finished") {
  return render(<StaffStandingsView contestId={ID} status={status} standings={value} dict={dict} locale="en" />);
}

describe("the staff table", () => {
  const t = () => dict.leaderboard.staff;

  beforeEach(() => {
    fetchStaffStandingsAction.mockReset();
    fetchStaffStandingsAction.mockResolvedValue({ kind: "ok", standings: board() });
  });

  test("names people twice over, marks the disqualified, and says what everybody else sees", () => {
    show(board(), "running");

    const row = screen.getByText("James Moriarty").closest("tr") as HTMLElement;
    expect(within(row).getByText("moriarty")).toBeInTheDocument();
    expect(within(row).getByText(t().disqualified)).toBeInTheDocument();
    expect(
      screen.getByText(t().shownFrozen.replace("{time}", formatTime("2026-09-20T11:30:00Z", { locale: "en" }))),
    ).toBeInTheDocument();
  });

  test("links the public page", () => {
    show(board(), "running");

    expect(screen.getByRole("link", { name: t().publicPage })).toHaveAttribute("href", `/contests/${ID}/leaderboard`);
  });

  test("offers the reveal only once a frozen contest has finished", () => {
    const { unmount } = show(board(), "running");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    unmount();

    const { unmount: again } = show(board({ freezeMin: null, shown: { state: "final", frozenAt: undefined } }), "finished");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    again();

    show(board({ revealedAt: "2026-09-20T13:00:00Z", shown: { state: "final", frozenAt: undefined } }), "finished");
    expect(screen.queryByRole("button", { name: t().reveal })).not.toBeInTheDocument();
    expect(
      screen.getByText(t().revealedAt.replace("{time}", formatTime("2026-09-20T13:00:00Z", { locale: "en" }))),
    ).toBeInTheDocument();
  });

  test("asks before revealing, because it cannot be undone", async () => {
    const user = userEvent.setup();
    show(board(), "finished");

    await user.click(screen.getByRole("button", { name: t().reveal }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(t().revealBody)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: t().revealConfirm })).toBeInTheDocument();
  });

  test("uses a fixed table layout so a long name truncates rather than growing the table", () => {
    show(board(), "running");

    const table = screen.getByRole("table");
    expect(table).toHaveClass("table-fixed");
    expect(screen.getByText("Sherlock Holmes")).toHaveClass("truncate");
  });

  test("shows the ICPC grid, solved and penalty, with no points column", () => {
    show(
      board({
        scoring: "icpc",
        questions: ["A", "B"],
        rows: [
          {
            place: 1,
            login: "holmes",
            fullName: "Sherlock Holmes",
            deleted: false,
            disqualified: false,
            points: 0,
            solved: 1,
            penalty: 12,
            cells: [
              { state: "solved", attempts: 1, minute: 12, first: true },
              { state: "untried" },
            ],
            lastScoredAt: undefined,
            winner: false,
          },
        ],
      }),
      "running",
    );

    expect(dict.leaderboard.columns.penalty).toBeTruthy();
    expect(screen.getByText(dict.leaderboard.columns.penalty)).toBeInTheDocument();
    expect(screen.queryByText(dict.leaderboard.columns.points)).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "A" })).toBeInTheDocument();
    expect(screen.getByText("A: solved at minute 12 on attempt 1, first to solve")).toBeInTheDocument();
    expect(screen.getByText("B: untried")).toBeInTheDocument();
  });

  test("gives the table a min-width variable scoped to the narrow breakpoint, not an unconditional one", () => {
    const questions = Array.from({ length: 12 }, (_, i) => String.fromCharCode(65 + i));
    show(
      board({
        scoring: "icpc",
        questions,
        rows: [
          {
            place: 1,
            login: "holmes",
            fullName: "Sherlock Holmes",
            deleted: false,
            disqualified: false,
            points: 0,
            solved: 1,
            penalty: 10,
            cells: questions.map(() => ({ state: "untried" as const })),
            lastScoredAt: undefined,
            winner: false,
          },
        ],
      }),
      "running",
    );

    const table = screen.getByRole("table");
    // Place (w-16 = 4rem) + login (w-28 = 7rem) + solved (w-20 = 5rem)
    // + penalty (w-20 = 5rem) + 12 grid cells (w-12 = 3rem each) + a
    // still-legible 12rem name column. The grid (and the login column) are
    // `max-narrow:hidden`, so an unconditional `min-width` would force a
    // sideways scroll on a phone showing none of the columns it is sized for.
    expect(table.style.getPropertyValue("--grid-min-width")).toBe("69rem");
    expect(table.className).toContain("narrow:min-w-(--grid-min-width)");
    expect(table.style.minWidth).toBe("");
  });

  test("sets no grid-width variable or class in points mode, which has no grid", () => {
    show(board(), "running");

    const table = screen.getByRole("table");
    expect(table.style.getPropertyValue("--grid-min-width")).toBe("");
    expect(table.className).not.toContain("--grid-min-width");
  });
});

describe("the staff table's own poll", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    fetchStaffStandingsAction.mockReset();
  });
  afterEach(() => vi.useRealTimers());

  test("refreshes only its own data while the contest runs, not the whole page", async () => {
    fetchStaffStandingsAction.mockResolvedValue({
      kind: "ok",
      standings: board({ rows: [{ ...board().rows[0], points: 77 }] }),
    });

    show(board(), "running");
    expect(screen.getByText("30")).toBeInTheDocument();
    expect(fetchStaffStandingsAction).not.toHaveBeenCalled();

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));

    expect(fetchStaffStandingsAction).toHaveBeenCalledWith(ID);
    expect(screen.getByText("77")).toBeInTheDocument();
    expect(screen.queryByText("30")).not.toBeInTheDocument();
  });

  test("does not poll a contest that is not running", async () => {
    fetchStaffStandingsAction.mockResolvedValue({ kind: "ok", standings: board() });
    show(board(), "finished");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS * 3));

    expect(fetchStaffStandingsAction).not.toHaveBeenCalled();
  });

  test("keeps the last copy and says so when a poll is refused", async () => {
    fetchStaffStandingsAction.mockResolvedValue({ kind: "refused", code: "leaderboard_too_often" });
    show(board(), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));

    expect(screen.getByText("30")).toBeInTheDocument();
    expect(screen.getByText(dict.leaderboard.failed)).toBeInTheDocument();
  });
});
