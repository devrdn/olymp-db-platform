import { describe, expect, test } from "vitest";

import { guardRedirect } from "./guard";

describe("guardRedirect", () => {
  test("sends a signed-out visitor to sign-in, remembering where they were going", () => {
    expect(guardRedirect("/contests", false)).toBe("/login?next=%2Fcontests");
  });

  test("leaves sign-in reachable without a session", () => {
    expect(guardRedirect("/login", false)).toBeNull();
  });

  test("lets a signed-in visitor through", () => {
    expect(guardRedirect("/contests", true)).toBeNull();
  });
});
