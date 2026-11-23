import { describe, expect, test, vi } from "vitest";

import { fill, isolate } from "./fill";

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

describe("isolate", () => {
  test("sets a name inside first-strong isolates", () => {
    expect(isolate("Ana Popescu")).toBe("\u2068Ana Popescu\u2069");
  });

  // A name that closes the isolate itself (U+2069), or carries any other
  // direction control, could otherwise reorder the sentence around it.
  test.each(["\u202A", "\u202B", "\u202C", "\u202D", "\u202E", "\u2066", "\u2067", "\u2068", "\u2069", "\u200E", "\u200F", "\u061C"])(
    "keeps no direction control %j of the name's own",
    (control) => {
      expect(isolate(`Ana${control}\u2069\u202Eesrever`)).toBe("\u2068Anaesrever\u2069");
    },
  );
});
