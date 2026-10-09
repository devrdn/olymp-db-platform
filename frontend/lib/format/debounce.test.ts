import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import { debounce } from "./debounce";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("debounce", () => {
  test("waits before doing anything at all", () => {
    const done = vi.fn();
    debounce(done, 300)("a");

    expect(done).not.toHaveBeenCalled();
    vi.advanceTimersByTime(299);
    expect(done).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(done).toHaveBeenCalledWith("a");
  });

  test("collapses a burst into one call carrying the last word", () => {
    const done = vi.fn();
    const search = debounce(done, 300);

    for (const value of ["p", "po", "pop", "pope"]) {
      search(value);
      vi.advanceTimersByTime(100);
    }
    vi.advanceTimersByTime(300);

    expect(done).toHaveBeenCalledTimes(1);
    expect(done).toHaveBeenCalledWith("pope");
  });

  test("runs again once the typing has actually stopped", () => {
    const done = vi.fn();
    const search = debounce(done, 300);

    search("first");
    vi.advanceTimersByTime(300);
    search("second");
    vi.advanceTimersByTime(300);

    expect(done).toHaveBeenNthCalledWith(1, "first");
    expect(done).toHaveBeenNthCalledWith(2, "second");
  });

  test("can be called off, so a component that unmounts leaves nothing behind", () => {
    const done = vi.fn();
    const search = debounce(done, 300);

    search("half a word");
    search.cancel();
    vi.advanceTimersByTime(1000);

    expect(done).not.toHaveBeenCalled();
  });
});
