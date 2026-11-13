import { describe, expect, test } from "vitest";

import { externalStartHref, PROVIDER_IDS, PROVIDER_NAMES, type ProviderId } from "./external";

const START = "/api/v1/auth/microsoft/start";
const microsoftStartHref = (next: string | undefined) => externalStartHref("microsoft", next);

describe("microsoftStartHref", () => {
  test("starts with no return when there is nowhere to resume", () => {
    expect(microsoftStartHref(undefined)).toBe(START);
    expect(microsoftStartHref("")).toBe(START);
  });

  test("carries a same-site path, percent-encoded", () => {
    expect(microsoftStartHref("/contests?status=draft&page=2")).toBe(
      `${START}?return=%2Fcontests%3Fstatus%3Ddraft%26page%3D2`,
    );
  });

  test.each(PROVIDER_IDS)("points %s at its own start route", (provider) => {
    expect(externalStartHref(provider, undefined)).toBe(`/api/v1/auth/${provider}/start`);
    expect(externalStartHref(provider, "/contests?status=draft")).toBe(
      `/api/v1/auth/${provider}/start?return=%2Fcontests%3Fstatus%3Ddraft`,
    );
  });

  test.each(["google", "github"] as ProviderId[])("never passes a hostile return through for %s", (provider) => {
    expect(externalStartHref(provider, "//evil.example")).toBe(`/api/v1/auth/${provider}/start`);
    expect(externalStartHref(provider, "/login")).toBe(`/api/v1/auth/${provider}/start`);
  });

  test("names the providers in their fixed order, by brand", () => {
    expect(PROVIDER_IDS).toEqual(["microsoft", "google", "github"]);
    expect(PROVIDER_NAMES).toEqual({ microsoft: "Microsoft", google: "Google", github: "GitHub" });
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

    expect(href).toBe(START);
    expect(href).not.toContain("evil");
  });

  // The password path refuses these as destinations, and so must this one: the
  // sign-in form again, or its own `?error=`, after a sign-in that worked.
  test.each(["/login", "/login/x", "/login?error=external_signin_failed", "/login#top", "/"])(
    "never returns to %j, where a password sign-in would not go either",
    (next) => {
      expect(microsoftStartHref(next)).toBe(START);
    },
  );

  test("still returns to a path that merely starts with the same letters", () => {
    expect(microsoftStartHref("/loginhelp")).toBe(`${START}?return=%2Floginhelp`);
  });

  test("keeps the longest path the backend accepts", () => {
    const longest = `/${"a".repeat(511)}`;

    expect(microsoftStartHref(longest)).toBe(`${START}?return=${encodeURIComponent(longest)}`);
  });
});
