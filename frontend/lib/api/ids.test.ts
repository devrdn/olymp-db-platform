import { describe, expect, test } from "vitest";

import { isId } from "./ids";

const REAL = "6f1b7d2e-3a4c-4f8b-9c1d-2e5a7b8c9d01";

describe("isId", () => {
  test("accepts the identifier the API issues", () => {
    expect(isId(REAL)).toBe(true);
    expect(isId(REAL.toUpperCase())).toBe(true);
  });

  /**
   * This is the point of the function. An identifier arriving from a form is
   * interpolated into a request path, and a value carrying a slash addresses a
   * different endpoint than the one the code appears to call:
   * `/contests/${id}/enroll` with `id` set to `x/../../auth/logout` reaches
   * logout. `fetch` normalises `..` before the request leaves, so nothing
   * downstream ever sees the trick.
   */
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
