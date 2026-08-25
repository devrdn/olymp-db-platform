import { describe, expect, test } from "vitest";

import { destinationAfterLogin } from "./destination";

describe("destinationAfterLogin", () => {
  test("sends an account on a one-time password to the password form first", () => {
    expect(
      destinationAfterLogin({
        mustChangePassword: true,
        permissions: ["contest.create", "users.manage"],
      }),
    ).toBe("/password");
  });

  test("sends staff to the constructor", () => {
    expect(
      destinationAfterLogin({ mustChangePassword: false, permissions: ["contest.create"] }),
    ).toBe("/contests");
  });

  test("sends an account with no staff permission to the participant area", () => {
    expect(destinationAfterLogin({ mustChangePassword: false, permissions: [] })).toBe("/my");
  });
});
