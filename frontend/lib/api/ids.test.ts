import { describe, expect, test } from "vitest";

import { isId, pathSegment } from "./ids";

const REAL = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

describe("isId", () => {
  test("accepts the identifier the API issues", () => {
    expect(isId(REAL)).toBe(true);
    expect(isId(REAL.toUpperCase())).toBe(true);
  });

  // `fetch` resolves `..` before sending, so the first of these would reach logout.
  test("refuses anything that could steer the request somewhere else", () => {
    for (const hostile of [
      "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01/../../auth/logout",
      "../users",
      "..",
      "/",
      `${REAL}/participants`,
      `${REAL}?scope=participant`,
      `${REAL}#fragment`,
      `${REAL}%2F..`,
      `${REAL}\n`,
    ]) {
      expect(isId(hostile)).toBe(false);
    }
  });

  test("refuses a value that is merely the wrong shape", () => {
    for (const wrong of ["", "42", "not-a-uuid", REAL.slice(0, -1), `${REAL}0`]) {
      expect(isId(wrong)).toBe(false);
    }
  });

  test("refuses anything that is not a string", () => {
    for (const wrong of [undefined, null, 42, {}, [REAL]]) {
      expect(isId(wrong)).toBe(false);
    }
  });
});

describe("pathSegment", () => {
  test("encodes a name as one segment of a request path", () => {
    expect(pathSegment("suspects")).toBe("suspects");
    expect(pathSegment("order lines/2026")).toBe("order%20lines%2F2026");
    // Escaped dots stay escaped, so `fetch` never reads them as a step up.
    expect(pathSegment("%2e%2e")).toBe("%252e%252e");
  });

  test("refuses a name that would move the request to another path, or leave it empty", () => {
    for (const name of [".", "..", ""]) expect(pathSegment(name)).toBeNull();
  });

  test("refuses anything a caller serialised that is not a well-formed string", () => {
    // `encodeURIComponent([".."])` is "..": a one-element array must not pass.
    for (const name of [[".."], ["."], 42, { toString: () => ".." }, null, undefined]) {
      expect(pathSegment(name)).toBeNull();
    }
    // A lone surrogate makes `encodeURIComponent` throw.
    expect(pathSegment("\ud800")).toBeNull();
  });
});
