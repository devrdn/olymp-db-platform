import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import type { FeedItem, FeedPage, Roster } from "@/lib/api/monitor";

const { fetchRoster, fetchFeed, refresh } = vi.hoisted(() => ({
  fetchRoster: vi.fn(),
  fetchFeed: vi.fn(),
  refresh: vi.fn(),
}));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchRoster,
  fetchFeed,
}));
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

import { feedItem, rosterRow } from "./test-fixtures";
import { MAX_CATCH_UP_PAGES, MAX_FAILURE_WAIT_MS, MONITOR_POLL_MS, useMonitor } from "./use-monitor";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

function page(items: FeedItem[], more = false): FeedPage {
  return { items, more, newest: items.at(-1)?.cursor, oldest: items[0]?.cursor };
}

const roster: Roster = { generatedAt: "x", truncated: false, rows: [rosterRow("a"), rosterRow("b")] };

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
  Object.defineProperty(document, "hidden", { value: state === "hidden", configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

function mount(initial: FeedPage = page([feedItem("c1")])) {
  return renderHook(() => useMonitor({ contestId: CONTEST, roster, feed: initial }));
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  setVisibility("visible");
  fetchRoster.mockImplementation(async () => ({ ...roster, rows: [rosterRow("a"), rosterRow("b")] }));
  fetchFeed.mockImplementation(async () => page([]));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("the polling cadence", () => {
  test("asks the table and the feed once every five seconds, after the newest item", async () => {
    mount();
    expect(fetchRoster).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
    expect(fetchFeed).toHaveBeenCalledTimes(1);
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, expect.objectContaining({ after: "c1" }), expect.anything());

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
    expect(fetchFeed).toHaveBeenCalledTimes(2);
  });

  test("stops while the tab is hidden and resumes with one immediate poll", async () => {
    mount();
    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 6));
    expect(fetchRoster).not.toHaveBeenCalled();

    await act(async () => setVisibility("visible"));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
    expect(fetchFeed).toHaveBeenCalledTimes(1);

    // And then back to the ordinary cadence, not a second immediate one.
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS - 1));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
  });

  test("a page that says there is more asks again sooner", async () => {
    fetchFeed.mockResolvedValueOnce(page([feedItem("c2")], true));
    mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(fetchFeed).toHaveBeenCalledTimes(2);
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, expect.objectContaining({ after: "c2" }), expect.anything());
  });
});

describe("what a poll brings", () => {
  test("appends new items once and lights up whoever they belong to", async () => {
    fetchFeed
      .mockResolvedValueOnce(page([feedItem("c1"), feedItem("c2", { registrationId: "b" })]))
      .mockResolvedValue(page([]));
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.feed.items.map((i) => i.cursor)).toEqual(["c1", "c2"]);
    expect([...result.current.fresh]).toEqual(["b"]);

    // The light goes out on its own.
    await act(() => vi.advanceTimersByTimeAsync(4000));
    expect(result.current.fresh.size).toBe(0);
  });

  test("an unchanged table keeps the same rows", async () => {
    const { result } = mount();
    const before = result.current.rows;

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.rows).toBe(before);
  });

  /**
   * A query arrives as `running`, and the feed does not deliver it again. The
   * next poll asks for that participant's running queries by time, and the
   * finished one takes its place.
   */
  test("refreshes a query that was still running", async () => {
    const at = "2026-09-20T10:00:01.100Z";
    const runningItem = feedItem("c2", { registrationId: "a", at });
    const running: FeedItem = {
      ...runningItem,
      detail: { ...(runningItem.detail as Extract<FeedItem["detail"], { type: "query" }>), status: "running" },
    };
    const done = feedItem("c2", { registrationId: "a", at });
    fetchFeed.mockImplementation(async (_contest: string, params: { participant?: string }) =>
      params.participant ? page([done]) : page([]),
    );
    const { result } = mount(page([feedItem("c1"), running]));

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));

    expect(fetchFeed).toHaveBeenCalledWith(
      CONTEST,
      { participant: "a", kinds: ["query"], from: at, until: "2026-09-20T10:00:01.101Z", limit: 200 },
      expect.anything(),
    );
    expect(result.current.feed.items[1]).toBe(done);
  });
});

describe("refusals", () => {
  test("a 429 waits as long as the server said before asking again", async () => {
    fetchRoster.mockRejectedValueOnce(new ApiError("monitor_too_often", 429, "slow", undefined, undefined, undefined, 60));
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.problem).toEqual({ kind: "tooOften", seconds: 60 });

    await act(() => vi.advanceTimersByTimeAsync(59_000));
    expect(fetchRoster).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
    expect(result.current.problem).toBeNull();
  });

  test("a 429 is not cut short by the tab becoming visible again", async () => {
    fetchRoster.mockRejectedValueOnce(new ApiError("monitor_too_often", 429, "slow", undefined, undefined, undefined, 60));
    mount();
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));

    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    await act(async () => setVisibility("visible"));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetchRoster).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(50_000));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
  });

  test("a 403 stops polling and says so", async () => {
    fetchRoster.mockRejectedValueOnce(new ApiError("forbidden", 403, "no"));
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.problem).toEqual({ kind: "forbidden" });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 10));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
  });

  test("any other failure is shown and retried on the ordinary cadence", async () => {
    fetchFeed.mockRejectedValueOnce(new Error("network"));
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.problem).toEqual({ kind: "failed" });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.problem).toBeNull();
  });

  test("a lost session hands over to the layout", async () => {
    fetchRoster.mockRejectedValueOnce(new ApiError("unauthenticated", 401, "who"));
    mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(refresh).toHaveBeenCalled();
  });
});

describe("the organiser's own moves", () => {
  test("a kind filter reads the newest page of those kinds afresh and polls after it", async () => {
    fetchFeed.mockResolvedValueOnce(page([feedItem("q9")]));
    const { result } = mount();

    await act(() => result.current.setKinds(["query"]));
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, { kinds: ["query"], limit: 200 }, expect.anything());
    expect(result.current.feed.items.map((i) => i.cursor)).toEqual(["q9"]);

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, expect.objectContaining({ after: "q9", kinds: ["query"] }), expect.anything());
  });

  test("loads older items before the oldest held", async () => {
    fetchFeed.mockResolvedValueOnce(page([feedItem("c0")]));
    const { result } = mount(page([feedItem("c1")], true));

    await act(() => result.current.loadOlder());
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, { before: "c1", kinds: [], limit: 200 }, expect.anything());
    expect(result.current.feed.items.map((i) => i.cursor)).toEqual(["c0", "c1"]);
    expect(result.current.feed.olderAvailable).toBe(false);
  });
});

/**
 * A tab hidden for an hour comes back thousands of items behind. Paging
 * through all of them would spend most of the read budget to show a list
 * that keeps only the last thousand anyway.
 */
describe("catching up after a long absence", () => {
  function pageOf(prefix: string, n: number, more: boolean): FeedPage {
    const items = Array.from({ length: n }, (_, i) => feedItem(`${prefix}${i}`, { registrationId: "b" }));
    return page(items, more);
  }

  test("reads only the feed on catch-up ticks, lights nothing, then gives up and reads the newest page", async () => {
    let n = 0;
    fetchFeed.mockImplementation(async (_contest: string, params: { after?: string }) => {
      if (params.after) return pageOf(`p${n++}-`, 200, true);
      return page([feedItem("latest", { registrationId: "b" })]);
    });
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
    expect(result.current.fresh.size).toBe(0);

    for (let i = 0; i < MAX_CATCH_UP_PAGES; i += 1) {
      await act(() => vi.advanceTimersByTimeAsync(1000));
    }

    // One ordinary read of the table, then only feed pages.
    expect(fetchRoster).toHaveBeenCalledTimes(1);
    expect(fetchFeed).toHaveBeenLastCalledWith(CONTEST, { kinds: [], limit: 200 }, expect.anything());
    expect(result.current.feed.items.map((i) => i.cursor)).toEqual(["latest"]);
    expect(result.current.feed.gap).toBe((MAX_CATCH_UP_PAGES + 1) * 200);
    expect(result.current.fresh.size).toBe(0);

    // And back to the ordinary cadence.
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
  });

  test("a short gap is read through and lights nothing", async () => {
    fetchFeed
      .mockResolvedValueOnce(pageOf("a", 200, true))
      .mockResolvedValueOnce(pageOf("b", 3, false))
      .mockResolvedValue(page([]));
    const { result } = mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    await act(() => vi.advanceTimersByTimeAsync(1000));

    expect(result.current.feed.items).toHaveLength(204);
    expect(result.current.feed.gap).toBe(0);
    expect(result.current.fresh.size).toBe(0);
  });
});

describe("waiting after refusals and failures", () => {
  test("a 429 on loading older holds the polls back too", async () => {
    fetchFeed.mockRejectedValueOnce(new ApiError("monitor_too_often", 429, "slow", undefined, undefined, undefined, 60));
    const { result } = mount(page([feedItem("c1")], true));

    await act(() => result.current.loadOlder());
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 11));
    expect(fetchRoster).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(1);
  });

  test("backs off on repeated server failures, and back to the cadence on success", async () => {
    const down = () => new ApiError("internal_error", 500, "down");
    fetchRoster
      .mockRejectedValueOnce(down())
      .mockRejectedValueOnce(down())
      .mockRejectedValueOnce(down())
      .mockImplementation(async () => roster);
    mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS)); // t=5: fails, waits 5
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS)); // t=10: fails, waits 10
    expect(fetchRoster).toHaveBeenCalledTimes(2);
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS)); // t=20: fails, waits 20
    expect(fetchRoster).toHaveBeenCalledTimes(3);
    await act(() => vi.advanceTimersByTimeAsync(20_000)); // t=40: succeeds
    expect(fetchRoster).toHaveBeenCalledTimes(4);
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(5);
  });

  test("never waits longer than the ceiling", async () => {
    fetchRoster.mockRejectedValue(new ApiError("internal_error", 500, "down"));
    mount();

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    for (let i = 0; i < 6; i += 1) await act(() => vi.advanceTimersByTimeAsync(MAX_FAILURE_WAIT_MS));
    const calls = fetchRoster.mock.calls.length;
    await act(() => vi.advanceTimersByTimeAsync(MAX_FAILURE_WAIT_MS));
    expect(fetchRoster.mock.calls.length).toBe(calls + 1);
  });
});

describe("returning to the tab", () => {
  test("does not poll at once when the last poll has just finished", async () => {
    mount();
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchRoster).toHaveBeenCalledTimes(1);

    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(500));
    await act(async () => setVisibility("visible"));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetchRoster).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS - 500));
    expect(fetchRoster).toHaveBeenCalledTimes(2);
  });
});

test("abandons reads still in flight when the screen goes away", async () => {
  let signal: AbortSignal | undefined;
  fetchRoster.mockImplementation((_contest: string, options: { signal?: AbortSignal }) => {
    signal = options.signal;
    return new Promise(() => {});
  });
  const { unmount } = mount();

  await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
  expect(signal?.aborted).toBe(false);

  unmount();
  expect(signal?.aborted).toBe(true);
});
