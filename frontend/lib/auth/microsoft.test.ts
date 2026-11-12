import { describe, expect, test } from "vitest";

import { MICROSOFT_START, microsoftStartHref } from "./microsoft";

describe("microsoftStartHref", () => {
  test("starts with no return when there is nowhere to resume", () => {
    expect(microsoftStartHref(undefined)).toBe(MICROSOFT_START);
    expect(microsoftStartHref("")).toBe(MICROSOFT_START);
  });

  test("carries a same-site path, percent-encoded", () => {
    expect(microsoftStartHref("/contests?status=draft&page=2")).toBe(
      `${MICROSOFT_START}?return=%2Fcontests%3Fstatus%3Ddraft%26page%3D2`,
    );
  });

  test("points at the API's start route", () => {
    expect(MICROSOFT_START).toBe("/api/v1/auth/microsoft/start");
  });

  // The backend falls back to `/` for these; the link must not carry them at all.
  test.each([
    ["another host, protocol-relative", "//evil.example"],
    ["an absolute URL", "https://evil.example/"],
    ["a backslash browsers read as a slash", "/\\evil.example"],
    ["a backslash later in the path", "/contests\\..\\x"],
    ["a space", "/my contests"],
    ["a tab", "/my\tcontests"],
    ["a newline", "/my\r\nSet-Cookie: x=1"],
    ["a character outside printable ASCII", "/contests/é"],
    ["no leading slash", "contests"],
    ["a path longer than the backend keeps", `/${"a".repeat(512)}`],
  ])("never passes %s through", (_, next) => {
    const href = microsoftStartHref(next);

    expect(href).toBe(MICROSOFT_START);
    expect(href).not.toContain("evil");
  });

  test("keeps the longest path the backend accepts", () => {
    const longest = `/${"a".repeat(511)}`;

    expect(microsoftStartHref(longest)).toBe(`${MICROSOFT_START}?return=${encodeURIComponent(longest)}`);
  });
});
