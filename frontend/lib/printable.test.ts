import { describe, expect, test } from "vitest";

import { printable } from "./printable";

describe("printable", () => {
  test("turns every character a log viewer breaks a line on into a space", () => {
    const breaks = ["\n", "\r", "\u0085", "\u2028", "\u2029"];

    for (const character of breaks) {
      expect(printable(`one${character}two`)).toBe("one two");
    }
  });

  test("keeps ordinary text, other scripts included, as it was", () => {
    expect(printable("Олимпиада №1 · Concurs")).toBe("Олимпиада №1 · Concurs");
  });

  test("bounds a line to five hundred characters", () => {
    expect(printable("a".repeat(600))).toHaveLength(500);
  });
});
