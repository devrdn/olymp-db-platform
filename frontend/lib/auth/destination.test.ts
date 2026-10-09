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

// An unchecked `next` would make sign-in an open redirect.
describe("destinationAfterLogin, resuming an interrupted journey", () => {
  const staff = { mustChangePassword: false, permissions: ["contest.create"] };

  test("returns to the page the visitor was asking for", () => {
    expect(destinationAfterLogin(staff, "/contests/3f1a/questions")).toBe(
      "/contests/3f1a/questions",
    );
  });

  test("keeps the query the visitor had", () => {
    expect(destinationAfterLogin(staff, "/contests?status=draft")).toBe("/contests?status=draft");
  });

  test.each([
    ["an absolute URL", "https://evil.example/steal"],
    ["a protocol-relative URL", "//evil.example/steal"],
    ["a backslash the browser normalises to a slash", "/\\evil.example/steal"],
    ["a scheme with no slashes", "javascript:alert(1)"],
    ["a path that is not a path", "contests"],
    ["an empty value", ""],
  ])("refuses %s and falls back to the default", (_case, next) => {
    expect(destinationAfterLogin(staff, next)).toBe("/contests");
  });

  test("refuses to send the visitor back to the sign-in page", () => {
    expect(destinationAfterLogin(staff, "/login")).toBe("/contests");
    expect(destinationAfterLogin(staff, "/login?next=%2Fcontests")).toBe("/contests");
  });

  test("refuses the root: signing in asks for work, not for the showcase", () => {
    expect(destinationAfterLogin(staff, "/")).toBe("/contests");
  });

  test("ignores it entirely while a one-time password is still in force", () => {
    expect(
      destinationAfterLogin({ mustChangePassword: true, permissions: ["contest.create"] },
        "/contests"),
    ).toBe("/password");
  });
});
