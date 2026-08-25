import { describe, expect, test } from "vitest";

import { formatDay, formatMoment, formatTime, isSameDay } from "./datetime";

/**
 * Every case is pinned to the installation's timezone rather than the
 * runtime's: the same instant is a different calendar day in Chisinau and in
 * UTC, and a register that disagreed with the clock on the wall would be
 * wrong in exactly the way nobody checks.
 */
describe("formatMoment", () => {
  test("renders a UTC instant in the contest's timezone, not the machine's", () => {
    // 19:00 UTC on 8 November is 21:00 in Chisinau.
    expect(formatMoment("2026-11-08T19:00:00Z", { timeZone: "Europe/Chisinau" })).toBe(
      "8 нояб. 2026 г., 21:00",
    );
  });
});

describe("isSameDay", () => {
  test("is true for a window that starts and ends the same evening locally", () => {
    expect(isSameDay("2026-11-08T19:00:00Z", "2026-11-08T21:00:00Z")).toBe(true);
  });

  test("is false once the window crosses local midnight", () => {
    expect(isSameDay("2026-11-08T19:00:00Z", "2026-11-09T03:00:00Z")).toBe(false);
  });

  test("uses the contest's timezone, not UTC", () => {
    // 22:30Z is already the next day in Chisinau (UTC+2), and still the same
    // day in UTC. The answer has to follow where the contest is held.
    expect(isSameDay("2026-11-08T21:00:00Z", "2026-11-08T22:30:00Z", { timeZone: "UTC" })).toBe(
      true,
    );
    expect(isSameDay("2026-11-08T21:00:00Z", "2026-11-08T22:30:00Z")).toBe(false);
  });
});

describe("formatting", () => {
  test("the day carries no time and the time carries no day", () => {
    const day = formatDay("2026-11-08T19:00:00Z", { locale: "en-GB" });
    const time = formatTime("2026-11-08T19:00:00Z", { locale: "en-GB" });

    expect(day).toMatch(/2026/);
    expect(day).not.toMatch(/\d{2}:\d{2}/);
    expect(time).toMatch(/\d{2}:\d{2}/);
    expect(time).not.toMatch(/2026/);
  });

  test("the full moment carries both", () => {
    const moment = formatMoment("2026-11-08T19:00:00Z", { locale: "en-GB" });

    expect(moment).toMatch(/2026/);
    expect(moment).toMatch(/\d{2}:\d{2}/);
  });
});
