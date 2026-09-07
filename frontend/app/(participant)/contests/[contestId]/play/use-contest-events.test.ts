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
  url: string;
  closed = false;
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

  close() {
    this.closed = true;
  }
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
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
});
