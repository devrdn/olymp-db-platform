import { describe, expect, test } from "vitest";

import { parseSetCookie } from "./cookie";

describe("parseSetCookie", () => {
  test("reads the session name, value and lifetime the API set", () => {
    const parsed = parseSetCookie(
      "dbcontest_session=s%3Aabc123; Path=/; Max-Age=43200; HttpOnly; SameSite=Lax",
    );

    expect(parsed).toEqual({
      name: "dbcontest_session",
      value: "s%3Aabc123",
      maxAge: 43200,
      path: "/",
    });
  });

  test("reports nothing for a header that carries no pair", () => {
    expect(parseSetCookie("Path=/; HttpOnly")).toBeNull();
  });
});
