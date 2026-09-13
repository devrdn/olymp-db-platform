import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { Standings } from "@/lib/api/leaderboard";

import { STANDINGS_POLL_MS, useStandings } from "./use-standings";

const LIVE_MS = STANDINGS_POLL_MS.live ?? 0;
const FROZEN_MS = STANDINGS_POLL_MS.frozen ?? 0;

function table(state: Standings["state"]): Standings {
  return {
    state, scoring: "points", title: "", frozenAt: undefined, endsAt: undefined,
    generatedAt: "2026-09-20T10:00:00Z", truncated: false, questions: undefined, rows: [],
  };
}

describe("useStandings", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  test("loads once it becomes active, and not before", async () => {
    const load = vi.fn().mockResolvedValue({ kind: "ok", standings: table("live") });
    const { rerender, result } = renderHook(({ active }) => useStandings({ load, active }), {
      initialProps: { active: false },
    });

    await act(async () => vi.advanceTimersByTimeAsync(LIVE_MS * 2));
    expect(load).not.toHaveBeenCalled();

    rerender({ active: true });
    await act(async () => vi.advanceTimersByTimeAsync(0));
    expect(load).toHaveBeenCalledTimes(1);
    expect(result.current.standings?.state).toBe("live");
  });

  test("polls a live table on its own interval and a final one not at all", async () => {
    const load = vi
      .fn()
      .mockResolvedValueOnce({ kind: "ok", standings: table("live") })
      .mockResolvedValue({ kind: "ok", standings: table("final") });
    renderHook(() => useStandings({ load, active: true }));

    await act(async () => vi.advanceTimersByTimeAsync(0));
    await act(async () => vi.advanceTimersByTimeAsync(LIVE_MS));
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () => vi.advanceTimersByTimeAsync(FROZEN_MS * 5));
    expect(load).toHaveBeenCalledTimes(2);
  });

  test("keeps the last copy and says so when a refresh fails", async () => {
    const load = vi
      .fn()
      .mockResolvedValueOnce({ kind: "ok", standings: table("live") })
      .mockResolvedValue({ kind: "refused", code: "leaderboard_too_often" });
    const { result } = renderHook(() => useStandings({ load, active: true }));

    await act(async () => vi.advanceTimersByTimeAsync(0));
    await act(async () => vi.advanceTimersByTimeAsync(LIVE_MS));
    expect(result.current.standings?.state).toBe("live");
    expect(result.current.failed).toBe(true);
  });

  test("starts from a copy it was given, and waits a full interval before asking again", async () => {
    const load = vi.fn().mockResolvedValue({ kind: "ok", standings: table("frozen") });
    const { result } = renderHook(() => useStandings({ load, active: true, initial: table("frozen") }));

    expect(result.current.standings?.state).toBe("frozen");
    await act(async () => vi.advanceTimersByTimeAsync(FROZEN_MS - 1));
    expect(load).not.toHaveBeenCalled();
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(load).toHaveBeenCalledTimes(1);
  });
});

describe("useStandings, against a server that answers from its cache", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  test("keeps polling when two reads come back identical", async () => {
    const same = table("live");
    const load = vi.fn().mockResolvedValue({ kind: "ok", standings: same });
    renderHook(() => useStandings({ load, active: true }));

    await act(async () => vi.advanceTimersByTimeAsync(0));
    for (let i = 0; i < 3; i++) {
      await act(async () => vi.advanceTimersByTimeAsync(LIVE_MS));
    }
    expect(load).toHaveBeenCalledTimes(4);
  });
});
