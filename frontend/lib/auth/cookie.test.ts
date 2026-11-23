import { describe, expect, test } from "vitest";

import { cookiePair, parseSetCookie } from "./cookie";

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

describe("cookiePair", () => {
  test("puts a cookie the browser holds into a Cookie header", () => {
    expect(cookiePair("dbcontest_device", "AQID.c2ln")).toBe("dbcontest_device=AQID.c2ln");
  });

  test("drops a value a header cannot carry, a line break above all", () => {
    // Next decodes cookie values, so `%0A` arrives as a real line break.
    const forged = [
      "abc\nFAKE LINE",
      decodeURIComponent("abc%0AFAKE LINE"),
      "abc\r",
      "a;b",
      "a b",
      'a"b',
      "a\\b",
      "",
      undefined,
    ];
    for (const value of forged) expect(cookiePair("dbcontest_session", value)).toBeNull();
  });
});
