import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { FeedItem, FeedPage, Roster } from "@/lib/api/monitor";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchRoster, fetchFeed } = vi.hoisted(() => ({ fetchRoster: vi.fn(), fetchFeed: vi.fn() }));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchRoster,
  fetchFeed,
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh: vi.fn() }) }));
// Every participant row formats its time away once per render, so counting
// the calls counts the rows that rendered.
const { readableDuration } = vi.hoisted(() => ({ readableDuration: vi.fn() }));
vi.mock("@/lib/format/bytes", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/format/bytes")>();
  readableDuration.mockImplementation(actual.readableDuration);
  return { ...actual, readableDuration };
});

import { MonitorView } from "./monitor-view";
import { feedItem, rosterRow } from "./test-fixtures";
import { MONITOR_POLL_MS } from "./use-monitor";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

function page(items: FeedItem[]): FeedPage {
  return { items, more: false, newest: items.at(-1)?.cursor, oldest: items[0]?.cursor };
}

function roster(): Roster {
  return {
    generatedAt: "x",
    truncated: false,
    rows: [rosterRow("a", { fullName: "Ivan Ivanov", queries: 3 }), rosterRow("b", { fullName: "Anna Petrova" })],
  };
}

beforeEach(() => {
  vi.useFakeTimers();
  fetchRoster.mockReset();
  fetchFeed.mockReset();
  readableDuration.mockClear();
  fetchRoster.mockImplementation(async () => roster());
  fetchFeed.mockImplementation(async () => page([]));
});

afterEach(() => {
  vi.useRealTimers();
});

function renderView() {
  return render(
    <MonitorView contestId={CONTEST} roster={roster()} feed={page([feedItem("c1")])} dict={dict} locale="en" />,
  );
}

describe("the monitoring screen", () => {
  test("offers the whole contest as CSV, as a link to the API", () => {
    renderView();

    expect(screen.getByRole("link", { name: dict.workspace.monitor.export.label })).toHaveAttribute(
      "href",
      `/api/v1/contests/${CONTEST}/monitor/export.csv`,
    );
  });

  test("says what it cannot see", () => {
    renderView();

    expect(screen.getByText(dict.workspace.monitor.notes.hints)).toBeInTheDocument();
    expect(screen.getByText(dict.workspace.monitor.notes.absenceAtEnd)).toBeInTheDocument();
  });

  /**
   * The screen is polled every five seconds for as long as a contest runs,
   * and two hundred rows re-rendered for every poll that brought nothing is
   * the cost this must not pay.
   */
  test("renders no row again for a poll that brought nothing new", async () => {
    renderView();
    expect(readableDuration).toHaveBeenCalledTimes(2);
    readableDuration.mockClear();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));

    expect(fetchRoster).toHaveBeenCalledTimes(1);
    expect(readableDuration).not.toHaveBeenCalled();
  });

  test("renders only the row that changed", async () => {
    fetchRoster.mockResolvedValueOnce({ ...roster(), rows: [roster().rows[0], rosterRow("b", { fullName: "Anna Petrova", queries: 1 })] });
    renderView();
    readableDuration.mockClear();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));

    expect(readableDuration).toHaveBeenCalledTimes(1);
  });

  test("lights a participant's row when something of theirs arrives", async () => {
    fetchFeed.mockResolvedValueOnce(page([feedItem("c2", { registrationId: "b", fullName: "Anna Petrova" })]));
    renderView();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));

    expect(screen.getByRole("row", { name: /Anna Petrova/ })).toHaveAttribute("data-fresh", "true");
    expect(screen.getByRole("row", { name: /Ivan Ivanov/ })).not.toHaveAttribute("data-fresh");
  });

  test("says when monitoring was taken away", async () => {
    const { ApiError } = await import("@/lib/api/client");
    fetchRoster.mockRejectedValueOnce(new ApiError("forbidden", 403, "no"));
    renderView();
    // The live region is there, empty, before anything goes wrong: a region
    // that appears together with its text is not announced by every reader.
    expect(screen.getByRole("status")).toHaveTextContent(/^$/);

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(screen.getByRole("status")).toHaveTextContent(dict.workspace.monitor.problems.forbidden);
  });

  test("says how long it will wait after too many reads", async () => {
    const { ApiError } = await import("@/lib/api/client");
    fetchRoster.mockRejectedValueOnce(new ApiError("monitor_too_often", 429, "slow", undefined, undefined, undefined, 42));
    renderView();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(screen.getByText(dict.workspace.monitor.problems.tooOften.replace("{seconds}", "42"))).toBeInTheDocument();
  });
});
