import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { useContestEvents } from "./use-contest-events";

/**
 * jsdom has no EventSource. This fake is the smallest thing that lets a test
 * dispatch the three event names the real channel ever sends
 * (events_handler.go's own doc) and prove the hook reacts to each — and prove
 * it closes exactly once, which is the whole point of a hook that promises to
 * leave no open connection behind.
 */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  // The three readyState values the real EventSource defines — CLOSED is the
  // one this hook actually branches on (a non-200 response fails the
  // connection permanently, per the SSE spec), so the fake carries all three
  // rather than just the one value a test happens to need today.
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;

  url: string;
  closed = false;
  readyState: number = FakeEventSource.CONNECTING;
  private listeners = new Map<string, Set<(event: MessageEvent) => void>>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, handler: (event: MessageEvent) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(handler);
  }

  removeEventListener(type: string, handler: (event: MessageEvent) => void) {
    this.listeners.get(type)?.delete(handler);
  }

  emit(type: string, data: unknown) {
    const payload = { data: JSON.stringify(data) } as MessageEvent;
    for (const handler of this.listeners.get(type) ?? []) handler(payload);
  }

  /** Fails this connection the way a non-200 response does: readyState moves to CLOSED, then `error` fires. */
  failPermanently() {
    this.readyState = FakeEventSource.CLOSED;
    this.emit("error", {});
  }

  /** Drops the connection the way a network blip does: the browser itself keeps retrying, so readyState stays CONNECTING. */
  dropTransiently() {
    this.readyState = FakeEventSource.CONNECTING;
    this.emit("error", {});
  }

  close() {
    this.closed = true;
  }
}

/** A fetch response `diagnose` (use-contest-events.ts) can read the API's own error envelope from. */
function apiResponse(status: number, code: string): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => JSON.stringify({ error: { code } }),
  } as Response;
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  vi.useFakeTimers();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("useContestEvents", () => {
  test("starts in the waiting phase and moves to running on contest_started", () => {
    const { result } = renderHook(() => useContestEvents("c1"));
    expect(result.current.phase).toBe("waiting");

    act(() => FakeEventSource.instances[0].emit("contest_started", { status: "running" }));

    expect(result.current.phase).toBe("running");
  });

  test("moves to finished on contest_finished", () => {
    const { result } = renderHook(() => useContestEvents("c1"));

    act(() => FakeEventSource.instances[0].emit("contest_finished", { status: "finished" }));

    expect(result.current.phase).toBe("finished");
  });

  test("computes the offset and deadline from a sync event without re-rendering for it", () => {
    const { result } = renderHook(() => useContestEvents("c1"));
    const before = result.current;

    act(() =>
      FakeEventSource.instances[0].emit("sync", {
        server_now: "2026-01-01T00:00:30.000Z",
        deadline: "2026-01-01T00:10:00.000Z",
      }),
    );

    // Same phase, same render: a sync updates the refs in place, which is
    // exactly what lets a once-a-second caller read a fresh value without
    // this hook itself causing a render on every thirty-second resync.
    expect(result.current).toBe(before);
    expect(result.current.deadlineRef.current).toBe(Date.parse("2026-01-01T00:10:00.000Z"));
  });

  test("reads a sync with no deadline as none, not as stale", () => {
    const { result } = renderHook(() => useContestEvents("c1"));

    act(() =>
      FakeEventSource.instances[0].emit("sync", {
        server_now: "2026-01-01T00:00:30.000Z",
        deadline: "2026-01-01T00:10:00.000Z",
      }),
    );
    act(() => FakeEventSource.instances[0].emit("sync", { server_now: "2026-01-01T00:00:31.000Z" }));

    expect(result.current.deadlineRef.current).toBeNull();
  });

  test("closes its connection, and only its own, on unmount", () => {
    const { unmount } = renderHook(() => useContestEvents("c1"));
    const source = FakeEventSource.instances[0];

    unmount();

    expect(source.closed).toBe(true);
    expect(FakeEventSource.instances).toHaveLength(1);
  });

  test("opens exactly one connection across re-renders of the caller", () => {
    const { rerender } = renderHook(({ id }: { id: string }) => useContestEvents(id), {
      initialProps: { id: "c1" },
    });

    rerender({ id: "c1" });
    rerender({ id: "c1" });

    expect(FakeEventSource.instances).toHaveLength(1);
  });

  test("opens a fresh connection for a different contest", () => {
    const { rerender } = renderHook(({ id }: { id: string }) => useContestEvents(id), {
      initialProps: { id: "c1" },
    });

    rerender({ id: "c2" });

    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[0].closed).toBe(true);
  });

  // Finding 1: per the SSE spec, a non-200 response fails an EventSource
  // permanently — readyState becomes CLOSED and the browser never retries on
  // its own. Left unhandled, the clock this connection drives freezes
  // forever. These tests are what fails against the unfixed hook: it had no
  // `error` listener at all, so none of `channelError`, the reconnect, or the
  // phase-to-finished transition below ever happened.
  describe("a connection EventSource itself gives up on", () => {
    test("a transient drop is left entirely to the browser's own retry", async () => {
      const fetchMock = vi.fn();
      vi.stubGlobal("fetch", fetchMock);
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].dropTransiently();
        await vi.advanceTimersByTimeAsync(0);
      });

      // readyState stayed CONNECTING: this is the browser's own reconnect
      // attempt, not a fatal failure, so nothing here should have asked why.
      expect(fetchMock).not.toHaveBeenCalled();
      expect(result.current.channelError).toBeNull();
      expect(FakeEventSource.instances).toHaveLength(1);
    });

    test("a retryable refusal (too many connections) is shown and reconnects after a backoff", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(429, "too_many_connections")));
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(result.current.channelError).toBe("too_many_connections");
      // Not yet — the retry is on a backoff timer, not immediate.
      expect(FakeEventSource.instances).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(FakeEventSource.instances).toHaveLength(2);
    });

    test("a terminal refusal (no longer a participant) is shown and never retried", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(403, "not_a_participant")));
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(result.current.channelError).toBe("not_a_participant");

      // Reconnecting cannot change who this account is — proven by advancing
      // well past even the backoff ceiling and finding no new connection.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });

      expect(FakeEventSource.instances).toHaveLength(1);
    });

    test("a refusal because the contest is over sets phase to finished, not an error", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(409, "contest_finished")));
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(result.current.phase).toBe("finished");
      expect(result.current.channelError).toBeNull();
    });

    // Finding 3: an admitted probe used to reconnect synchronously, with the
    // backoff reset and no timer at all. If EventSource kept failing on this
    // exact URL while a plain fetch of it kept succeeding, that reconnected
    // as fast as the network allowed — and every attempt still spends the
    // query-rate budget this channel shares with the SQL console. This is
    // what fails against the unfixed hook: `connect()` ran inline in the
    // `.then` callback, so a new instance existed already at the first
    // assertion below, before any time had even been asked to pass.
    test("a failure that a fresh probe is actually admitted still waits for the floor before reconnecting", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue({
          ok: true,
          status: 200,
          text: async () => "",
          body: { cancel: async () => {} },
        }),
      );
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(result.current.channelError).toBeNull();
      // Not yet — an admitted probe still waits for the same floor a
      // retryable refusal does, so a flapping connection cannot reconnect
      // faster than the network allows.
      expect(FakeEventSource.instances).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(FakeEventSource.instances).toHaveLength(2);
    });

    test("a sync on the reconnected channel clears the error", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(429, "query_too_often")));
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(result.current.channelError).toBe("query_too_often");

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      act(() =>
        FakeEventSource.instances[1].emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }),
      );

      expect(result.current.channelError).toBeNull();
    });
  });
});
