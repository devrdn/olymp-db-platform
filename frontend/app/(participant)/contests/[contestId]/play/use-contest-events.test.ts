import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { useContestEvents } from "./use-contest-events";

/**
 * jsdom has no EventSource. This fake dispatches the three events the real
 * channel sends and records that the hook closes it exactly once.
 */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  // All three readyState values; the hook branches on CLOSED, which a
  // non-200 response sets.
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

  /** Fails like a non-200 response: readyState becomes CLOSED, then `error` fires. */
  failPermanently() {
    this.readyState = FakeEventSource.CLOSED;
    this.emit("error", {});
  }

  /** Drops like a network blip: the browser retries, so readyState stays CONNECTING. */
  dropTransiently() {
    this.readyState = FakeEventSource.CONNECTING;
    this.emit("error", {});
  }

  close() {
    this.closed = true;
  }
}

/** A response carrying the API's error envelope, for `diagnose`. */
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
  // Jitter would make fixed clock advances flaky, so it is pinned to zero
  // and every test measures the floor; the jitter test pins the other end.
  vi.spyOn(Math, "random").mockReturnValue(0);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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

    // A sync updates the refs in place and does not render.
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

  // A non-200 response fails an EventSource permanently, and the browser
  // never retries; left unhandled, the clock would freeze.
  describe("a connection EventSource itself gives up on", () => {
    test("a transient drop is left entirely to the browser's own retry", async () => {
      const fetchMock = vi.fn();
      vi.stubGlobal("fetch", fetchMock);
      const { result } = renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].dropTransiently();
        await vi.advanceTimersByTimeAsync(0);
      });

      // CONNECTING: the browser's own retry, so nothing asks why.
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
      // Not yet: the retry waits on a backoff timer.
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

      // Reconnecting cannot change who the account is: nothing reconnects even
      // past the backoff ceiling.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120_000);
      });

      expect(FakeEventSource.instances).toHaveLength(1);
    });

    test.each(["contest_finished", "contest_ended"])(
      "a refusal because the contest is over (%s) sets phase to finished, not an error",
      async (code) => {
        vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(409, code)));
        const { result } = renderHook(() => useContestEvents("c1"));

        await act(async () => {
          FakeEventSource.instances[0].failPermanently();
          await vi.advanceTimersByTimeAsync(0);
        });

        expect(result.current.phase).toBe("finished");
        expect(result.current.channelError).toBeNull();
      },
    );

    // A contest taken back to draft: refused until republished, and the screen
    // keeps waiting rather than announcing the end.
    test("a refusal because the contest is not open now is shown and reconnects, and never finishes", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(409, "contest_not_running")));
      const { result } = renderHook(() => useContestEvents("c1", "waiting"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });

      expect(result.current.phase).toBe("waiting");
      expect(result.current.channelError).toBe("contest_not_running");
      expect(FakeEventSource.instances).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(FakeEventSource.instances).toHaveLength(2);
      expect(result.current.phase).toBe("waiting");
    });

    // An individual window not yet open is refused as not open now; the first
    // accepted connection is the only sign it opened, so the hook reports it.
    test("after a refusal because the contest is not open now, the first accepted connection reports it reopened", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(409, "contest_not_running")));
      const { result } = renderHook(() => useContestEvents("c1", "running"));
      expect(result.current.reopened).toBe(false);

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(result.current.reopened).toBe(false);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      act(() => FakeEventSource.instances[1].emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }));

      expect(result.current.reopened).toBe(true);
      expect(result.current.channelError).toBeNull();
    });

    // The page may have rendered "not open now" while the channel's first
    // connection is simply admitted; told so by the caller, the hook reports
    // that connection as the reopening.
    test("a page rendered not open now reports the channel's first accepted connection as reopened, never refused", () => {
      const { result } = renderHook(() => useContestEvents("c1", "running", true));
      expect(result.current.reopened).toBe(false);

      act(() => FakeEventSource.instances[0].emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }));

      expect(result.current.reopened).toBe(true);
    });

    // The page streams in under the header, so it can say this after the
    // channel was admitted.
    test("a page that says it rendered not open now after the channel was admitted reports reopened then", () => {
      const { result, rerender } = renderHook(({ dormant }) => useContestEvents("c1", "running", dormant), {
        initialProps: { dormant: false },
      });
      act(() => FakeEventSource.instances[0].emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }));
      expect(result.current.reopened).toBe(false);

      rerender({ dormant: true });

      expect(result.current.reopened).toBe(true);
    });

    test("a page rendered not open now does not report reopened before any connection is accepted", () => {
      const { result } = renderHook(() => useContestEvents("c1", "running", true));

      expect(result.current.reopened).toBe(false);
    });

    test("a channel that was never refused as not open now does not report reopening on connect", async () => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(429, "query_too_often")));
      const { result } = renderHook(() => useContestEvents("c1", "running"));

      act(() => FakeEventSource.instances[0].emit("sync", { server_now: "2026-01-01T00:00:00.000Z" }));
      expect(result.current.reopened).toBe(false);

      // A refusal that passes by itself is not the contest opening.
      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      act(() => FakeEventSource.instances[1].emit("sync", { server_now: "2026-01-01T00:00:05.000Z" }));

      expect(result.current.reopened).toBe(false);
    });

    // An admitted probe must still wait for the floor: every reconnect spends
    // the query-rate budget shared with the SQL console.
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
      // Not yet: the admitted probe waits for the same floor.
      expect(FakeEventSource.instances).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });

      expect(FakeEventSource.instances).toHaveLength(2);
    });

    /** A probe the server keeps admitting while EventSource keeps failing. */
    function admittedProbe() {
      return vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        text: async () => "",
        body: { cancel: async () => {} },
      });
    }

    // This branch (a proxy that treats text/event-stream differently, say)
    // must double like the others; a fixed floor would reconnect every five
    // seconds, two rate-limit charges each time.
    test("an admitted probe that keeps failing backs off like every other reconnect", async () => {
      vi.stubGlobal("fetch", admittedProbe());
      renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(FakeEventSource.instances).toHaveLength(2);

      // The second failure: five seconds is now too soon, the wait doubled.
      await act(async () => {
        FakeEventSource.instances[1].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(FakeEventSource.instances).toHaveLength(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(FakeEventSource.instances).toHaveLength(3);
    });

    // The server's `retry:` is a flat thirty seconds, so clients cut by a
    // deploy would return together. With Math.random at its top value the wait
    // is the floor plus half, so five seconds is not enough.
    test("the reconnect delay carries jitter, so a deploy does not bring every client back at once", async () => {
      vi.spyOn(Math, "random").mockReturnValue(1);
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(apiResponse(429, "too_many_connections")));
      renderHook(() => useContestEvents("c1"));

      await act(async () => {
        FakeEventSource.instances[0].failPermanently();
        await vi.advanceTimersByTimeAsync(0);
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(FakeEventSource.instances).toHaveLength(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(2_500);
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

  // The content reads start an individual clock, possibly after the first
  // sync; one immediate resync after the workspace loads brings the deadline.
  describe("a one-shot resync", () => {
    test("reopens the channel once, and a second request does nothing", () => {
      const { result, unmount } = renderHook(() => useContestEvents("c1", "running"));
      expect(FakeEventSource.instances).toHaveLength(1);

      act(() => result.current.resync());
      expect(FakeEventSource.instances[0].closed).toBe(true);
      expect(FakeEventSource.instances).toHaveLength(2);

      act(() => result.current.resync());
      expect(FakeEventSource.instances).toHaveLength(2);

      const deadline = "2026-03-01T10:30:00Z";
      act(() => FakeEventSource.instances[1].emit("sync", { server_now: "2026-03-01T10:00:00Z", deadline }));
      expect(result.current.deadlineRef.current).toBe(new Date(deadline).getTime());

      unmount();
      expect(FakeEventSource.instances[1].closed).toBe(true);
    });

    test("does not open a connection of its own while the channel is already failed and waiting to reconnect", () => {
      const { result } = renderHook(() => useContestEvents("c1", "running"));
      FakeEventSource.instances[0].readyState = FakeEventSource.CLOSED;

      act(() => result.current.resync());

      expect(FakeEventSource.instances).toHaveLength(1);
    });
  });
});
