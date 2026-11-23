import { describe, expect, test, vi } from "vitest";

import { fill } from "./fill";

describe("fill", () => {
  test("replaces every occurrence of a placeholder", () => {
    expect(fill("{n} of {n}", { n: 3 })).toBe("3 of 3");
  });

  test("fills several placeholders at once", () => {
    expect(fill("{who} joined {contest}", { who: "Ana", contest: "Spring" })).toBe("Ana joined Spring");
  });

  test.each(["$&", "$'", "$`", "$1", "$$", "Q$&A 50$' off $1 $` $$"])("reads %s literally, never as a replacement pattern", (value) => {
    expect(fill("Delete “{title}”?", { title: value })).toBe(`Delete “${value}”?`);
  });

  test("does not fill a placeholder written inside a value", () => {
    expect(fill("{a} {b}", { a: "{b}", b: "x" })).toBe("{b} x");
  });

  test("throws under test when a placeholder has no value, so a mistyped key fails", () => {
    expect(() => fill("{n} items", { nn: 3 })).toThrow("{n}");
  });

  test("does not read a name from the object prototype", () => {
    expect(() => fill("{constructor}", {})).toThrow("{constructor}");
  });

  test("leaves a placeholder without a value visible outside test", () => {
    vi.stubEnv("NODE_ENV", "production");
    try {
      expect(fill("{a} {b}", { a: "1" })).toBe("1 {b}");
    } finally {
      vi.unstubAllEnvs();
    }
  });
});
