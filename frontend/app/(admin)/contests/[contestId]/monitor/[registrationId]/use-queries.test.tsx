import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { MAX_QUERY_SEARCH, type QueriesPage } from "@/lib/api/monitor";

const { fetchQueries, fetchTimeline } = vi.hoisted(() => ({ fetchQueries: vi.fn(), fetchTimeline: vi.fn() }));
vi.mock("@/lib/api/monitor", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/monitor")>()),
  fetchQueries,
  fetchTimeline,
}));

import { MONITOR_POLL_MS } from "../use-monitor";
import { CONTEST, loggedQuery, queryFeedItem, REG, setVisibility } from "./test-fixtures";
import { SEARCH_DEBOUNCE_MS, useQueries } from "./use-queries";

function mount(initial: QueriesPage = { items: [loggedQuery(9), loggedQuery(8)], more: true }) {
  return renderHook(() => useQueries({ contestId: CONTEST, registrationId: REG, initial }));
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetAllMocks();
  setVisibility("visible");
  fetchQueries.mockResolvedValue({ items: [], more: false });
  fetchTimeline.mockResolvedValue({ items: [], more: false });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("filters", () => {
  test("a status reads the first page of that status afresh", async () => {
    fetchQueries.mockResolvedValueOnce({ items: [loggedQuery(3, { status: "error" })], more: false });
    const { result } = mount();

    await act(() => result.current.setStatus("error"));
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "error", q: "" }, expect.anything());
    expect(result.current.items.map((q) => q.id)).toEqual([3]);
    expect(result.current.more).toBe(false);
  });

  test("a search waits for the typing to stop, then asks once", async () => {
    const { result } = mount();

    act(() => result.current.setSearch("s"));
    act(() => result.current.setSearch("se"));
    act(() => result.current.setSearch("sel"));
    expect(result.current.search).toBe("sel");
    await act(() => vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS - 1));
    expect(fetchQueries).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(fetchQueries).toHaveBeenCalledTimes(1);
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "", q: "sel" }, expect.anything());
  });

  test("a search is cut to the length the API takes", async () => {
    const { result } = mount();

    act(() => result.current.setSearch("x".repeat(MAX_QUERY_SEARCH + 50)));
    expect(result.current.search).toHaveLength(MAX_QUERY_SEARCH);
    await act(() => vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS));
    expect(fetchQueries).toHaveBeenCalledWith(
      CONTEST,
      REG,
      { status: "", q: "x".repeat(MAX_QUERY_SEARCH) },
      expect.anything(),
    );
  });

  test("an answer to an older filter is dropped", async () => {
    let answerOld: (page: QueriesPage) => void = () => {};
    fetchQueries
      .mockImplementationOnce(() => new Promise((resolve) => (answerOld = resolve)))
      .mockResolvedValueOnce({ items: [loggedQuery(2, { status: "ok" })], more: false });
    const { result } = mount();

    let first: Promise<void> = Promise.resolve();
    act(() => {
      first = result.current.setStatus("error");
    });
    await act(() => result.current.setStatus("ok"));
    await act(async () => {
      answerOld({ items: [loggedQuery(1, { status: "error" })], more: false });
      await first;
    });
    expect(result.current.items.map((q) => q.id)).toEqual([2]);
  });
});

describe("loading more", () => {
  test("asks after the last query held, with the same filters, and appends", async () => {
    fetchQueries.mockResolvedValueOnce({ items: [loggedQuery(7), loggedQuery(6)], more: false });
    const { result } = mount();

    await act(() => result.current.loadMore());
    expect(fetchQueries).toHaveBeenCalledWith(CONTEST, REG, { status: "", q: "", cursor: "q8" }, expect.anything());
    expect(result.current.items.map((q) => q.id)).toEqual([9, 8, 7, 6]);
    expect(result.current.more).toBe(false);
  });

  test("a refusal says to wait", async () => {
    fetchQueries.mockRejectedValueOnce(new ApiError("monitor_too_often", 429, "slow", undefined, undefined, undefined, 60));
    const { result } = mount();

    await act(() => result.current.loadMore());
    expect(result.current.problem).toEqual({ kind: "tooOften", seconds: 60 });
    expect(result.current.items).toHaveLength(2);
  });
});

/**
 * While a running query is on screen and the tab visible, its stretch of
 * timeline is polled and the outcome put in place; the statement stays.
 */
describe("running queries", () => {
  const at = "2026-09-20T10:00:01.100Z";

  test("are asked about again until they end", async () => {
    const running = loggedQuery(5, { status: "running", executedAt: at, durationMs: null, rowCount: null, sql: "SELECT pg_sleep(3)" });
    fetchTimeline.mockResolvedValue({
      items: [queryFeedItem({ ...running, status: "ok", durationMs: 3000, rowCount: 1, sql: "SELECT pg_sl" })],
      more: false,
    });
    const { result } = mount({ items: [running], more: false });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchTimeline).toHaveBeenCalledWith(
      CONTEST,
      REG,
      { kinds: ["query"], from: at, until: "2026-09-20T10:00:01.101Z", limit: 200 },
      expect.anything(),
    );
    expect(result.current.items[0]).toMatchObject({ status: "ok", durationMs: 3000, rowCount: 1, sql: "SELECT pg_sleep(3)" });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 3));
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
  });

  test("a refresh that answers clears the failure the one before it showed", async () => {
    const running = loggedQuery(5, { status: "running", executedAt: at });
    fetchTimeline.mockRejectedValueOnce(new Error("network")).mockResolvedValue({ items: [], more: false });
    const { result } = mount({ items: [running], more: false });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(result.current.problem).toEqual({ kind: "failed" });

    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS));
    expect(fetchTimeline).toHaveBeenCalledTimes(2);
    expect(result.current.problem).toBeNull();
  });

  test("nothing is asked while the tab is hidden, nor when nothing runs", async () => {
    mount();
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 3));
    expect(fetchTimeline).not.toHaveBeenCalled();

    const running = loggedQuery(5, { status: "running", executedAt: at });
    mount({ items: [running], more: false });
    await act(async () => setVisibility("hidden"));
    await act(() => vi.advanceTimersByTimeAsync(MONITOR_POLL_MS * 3));
    expect(fetchTimeline).not.toHaveBeenCalled();

    await act(async () => setVisibility("visible"));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetchTimeline).toHaveBeenCalledTimes(1);
  });
});
