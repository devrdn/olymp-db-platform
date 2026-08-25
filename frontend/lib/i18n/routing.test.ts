import { describe, expect, test } from "vitest";

import { localeRedirect } from "./routing";

describe("localeRedirect", () => {
  test("leaves a path that already names a supported locale alone", () => {
    expect(localeRedirect("/ro/contests", "ru,en;q=0.8")).toBeNull();
  });

  test("sends an unprefixed path to the language the browser asked for", () => {
    expect(localeRedirect("/contests", "ru-RU,ru;q=0.9,en;q=0.8")).toBe("/ru/contests");
  });

  test("falls back to English when the browser wants nothing we speak", () => {
    expect(localeRedirect("/contests", "de-DE,de;q=0.9")).toBe("/en/contests");
  });

  test("keeps the root path usable", () => {
    expect(localeRedirect("/", "ro-MD")).toBe("/ro");
  });
});
