import { act, renderHook } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { ClarificationSignalProvider, useClarificationPublisher, useClarificationSignal } from "./clarification-signal";

function useBoth() {
  return { signal: useClarificationSignal(), publish: useClarificationPublisher() };
}

describe("the clarification signal", () => {
  test("counts hints and connections, and turns finished once", () => {
    const { result } = renderHook(useBoth, { wrapper: ClarificationSignalProvider });
    const publisher = result.current.publish;

    act(() => {
      result.current.publish.hint();
      result.current.publish.hint();
      result.current.publish.connected();
      result.current.publish.finish();
      result.current.publish.finish();
    });

    expect(result.current.signal).toEqual({ hints: 2, connections: 1, finished: true });
    // The same functions every render, so the channel never reconnects for them.
    expect(result.current.publish).toBe(publisher);
  });

  test("outside a provider never changes and publishes nowhere", () => {
    const { result } = renderHook(useBoth);
    act(() => result.current.publish.hint());
    expect(result.current.signal).toEqual({ hints: 0, connections: 0, finished: false });
  });
});
