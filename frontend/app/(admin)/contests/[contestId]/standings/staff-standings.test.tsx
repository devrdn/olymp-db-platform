import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { StaffStandings } from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// One stable router object, as Next provides: the component lists `router` in
// an effect's dependencies.
const { fetchStaffStandingsAction, routerRefresh, router } = vi.hoisted(() => {
  const routerRefresh = vi.fn();
  return { fetchStaffStandingsAction: vi.fn(), routerRefresh, router: { refresh: routerRefresh } };
});
vi.mock("./actions", () => ({
  revealStandingsAction: vi.fn(async () => ({ revealedAt: "2026-09-20T13:00:00Z" })),
  fetchStaffStandingsAction,
}));
vi.mock("next/navigation", () => ({ useRouter: () => router }));

import { STAFF_REFRESH_MS, StaffStandingsView } from "./staff-standings";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const ID = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

function board(overrides: Partial<StaffStandings> = {}): StaffStandings {
  return {
    shown: { state: "frozen", frozenAt: "2026-09-20T11:30:00Z" },
    status: "running",
    scoring: "points",
    freezeMin: 30,
    names: "login",
    revealedAt: undefined,
    generatedAt: "2026-09-20T12:00:00Z",
    truncated: false,
    questions: undefined,
    rows: [
      { place: 1, prize: 1, login: "holmes", fullName: "Sherlock Holmes", deleted: false, disqualified: false, points: 30, solved: 3, penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false },
      { place: 2, prize: null, login: "moriarty", fullName: "James Moriarty", deleted: false, disqualified: true, points: 99, solved: 9, penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false },
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

  test("shows each row's place first, then its prize tier, and names the tier in words", () => {
    show(board(), "running");

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(0, 2)).toEqual([dict.leaderboard.columns.place, dict.leaderboard.columns.prize]);

    const holmesRow = screen.getByText("Sherlock Holmes").closest("tr") as HTMLElement;
    expect(within(holmesRow).getByTestId("place")).toHaveTextContent("1");
    const holmes = within(holmesRow).getByTestId("prize");
    expect(holmes).toHaveAttribute("data-medal", "gold");
    expect(within(holmes).getByText("I")).toBeInTheDocument();
    expect(within(holmes).getByText(dict.leaderboard.prizes.first)).toHaveClass("sr-only");

    const moriartyRow = screen.getByText("James Moriarty").closest("tr") as HTMLElement;
    expect(within(moriartyRow).getByTestId("place")).toHaveTextContent("2");
    const moriarty = within(moriartyRow).getByTestId("prize");
    expect(moriarty).not.toHaveAttribute("data-medal");
    expect(within(moriarty).getByText(dict.leaderboard.prizes.none)).toHaveClass("sr-only");
  });

  test("shows a dash for a row without a place, and no prize column when nobody has a prize", () => {
    const [first, second] = board().rows;
    show(
      board({ rows: [{ ...first, prize: null }, { ...second, place: null, points: 0, solved: 0 }] }),
      "running",
    );

    expect(screen.queryByRole("columnheader", { name: dict.leaderboard.columns.prize })).not.toBeInTheDocument();
    expect(screen.queryByTestId("prize")).not.toBeInTheDocument();
    const idle = within(screen.getByText("James Moriarty").closest("tr") as HTMLElement).getByTestId("place");
    expect(within(idle).getByText("—")).toHaveAttribute("aria-hidden");
    expect(within(idle).getByText(dict.leaderboard.unplaced)).toHaveClass("sr-only");
  });

  test("gives tied rows the number they share", () => {
    const [first, second] = board().rows;
    show(board({ rows: [{ ...first, place: 1 }, { ...second, place: 1 }] }), "running");

    for (const name of ["Sherlock Holmes", "James Moriarty"]) {
      expect(within(screen.getByText(name).closest("tr") as HTMLElement).getByTestId("place")).toHaveTextContent("1");
    }
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
            prize: null,
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
            prize: null,
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
    // Place 4 + login 7 + solved 5 + penalty 5 + 12 × 3 + name floor 12 (rem).
    // Applied only from `narrow` up, where those columns show.
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
    routerRefresh.mockReset();
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

  // `status` is a server prop the poll never updates, so without a refresh the
  // reveal button would never appear after the scheduler finishes the contest.
  test("asks the layout to refresh once the contest's own status has moved on, and stops once the new status arrives", async () => {
    fetchStaffStandingsAction.mockResolvedValue({
      kind: "ok",
      standings: board({ status: "finished" }),
    });
    const { rerender } = show(board(), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(routerRefresh).toHaveBeenCalledTimes(1);
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(1);

    // A refresh that brought no new props must not stop the chain.
    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(2);
    expect(routerRefresh).toHaveBeenCalledTimes(2);

    // The new status ends the chain through the effect's cleanup.
    rerender(<StaffStandingsView contestId={ID} status="finished" standings={board({ status: "finished" })} dict={dict} locale="en" />);
    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS * 3));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(2);
  });

  // A thrown action (network drop, retired action id) is a failed poll, not the
  // end of the chain.
  test("treats a poll that throws as a failure and keeps polling", async () => {
    fetchStaffStandingsAction.mockRejectedValueOnce(new Error("Failed to find Server Action"));
    fetchStaffStandingsAction.mockResolvedValue({
      kind: "ok",
      standings: board({ rows: [{ ...board().rows[0], points: 64 }] }),
    });
    show(board(), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(screen.getByText(dict.leaderboard.failed)).toBeInTheDocument();
    expect(screen.getByText("30")).toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(2);
    expect(screen.getByText("64")).toBeInTheDocument();
    expect(screen.queryByText(dict.leaderboard.failed)).not.toBeInTheDocument();
  });

  // A mid-contest freeze changes shown.state but not status, so no layout
  // refresh.
  test("does not refresh for an ordinary freeze reached mid-contest", async () => {
    fetchStaffStandingsAction.mockResolvedValue({
      kind: "ok",
      standings: board({ shown: { state: "frozen", frozenAt: "2026-09-20T11:30:00Z" } }),
    });
    show(board({ shown: { state: "live", frozenAt: undefined } }), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));

    expect(routerRefresh).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(2);
  });

  test("refreshes instead of showing a failure banner when a poll is unauthenticated", async () => {
    fetchStaffStandingsAction.mockResolvedValue({ kind: "refused", code: "unauthenticated" });
    show(board(), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));

    expect(routerRefresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(dict.leaderboard.failed)).not.toBeInTheDocument();
  });

  test("never overlaps polls and ignores a response older than the one already applied", async () => {
    let resolveFirst!: (value: { kind: "ok"; standings: StaffStandings }) => void;
    const first = new Promise<{ kind: "ok"; standings: StaffStandings }>((resolve) => {
      resolveFirst = resolve;
    });
    fetchStaffStandingsAction.mockReturnValueOnce(first);
    fetchStaffStandingsAction.mockResolvedValue({
      kind: "ok",
      standings: board({ rows: [{ ...board().rows[0], points: 55 }] }),
    });
    show(board(), "running");

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(1);

    // While the first request is pending no second one starts.
    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS * 2));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(1);

    // Resolving late, it is still the newest copy, and the chain resumes.
    await act(async () => {
      resolveFirst({ kind: "ok", standings: board({ rows: [{ ...board().rows[0], points: 99 }] }) });
    });
    expect(screen.getByText("99")).toBeInTheDocument();

    await act(async () => vi.advanceTimersByTimeAsync(STAFF_REFRESH_MS));
    expect(fetchStaffStandingsAction).toHaveBeenCalledTimes(2);
    expect(screen.getByText("55")).toBeInTheDocument();
  });
});
