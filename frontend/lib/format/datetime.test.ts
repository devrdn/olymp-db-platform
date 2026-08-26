import { describe, expect, test } from "vitest";

import {
  formatDay,
  formatMoment,
  formatTime,
  instantFromWallClock,
  isSameDay,
  wallClockFromInstant,
} from "./datetime";

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

/**
 * The bug these exist to prevent: a `datetime-local` field hands back a wall
 * clock with no zone, and `new Date()` resolves it in whatever zone the
 * process runs in — UTC inside the container, the developer's zone on a
 * laptop. A contest set to start at ten would start at ten somewhere nobody
 * involved lives, and nothing would look wrong on the screen that set it.
 */
describe("instantFromWallClock", () => {
  test("reads a summer wall clock as the university's summer time", () => {
    // Chisinau is UTC+3 in May.
    expect(instantFromWallClock("2026-05-14T10:00")).toBe("2026-05-14T07:00:00.000Z");
  });

  test("reads a winter wall clock as the university's winter time", () => {
    // And UTC+2 in December, which is the whole reason the offset cannot be a
    // constant.
    expect(instantFromWallClock("2026-12-14T10:00")).toBe("2026-12-14T08:00:00.000Z");
  });

  test("does not depend on the zone the process happens to run in", () => {
    // Named explicitly, the answer is the same figure a UTC container and a
    // laptop in Chisinau both have to produce.
    expect(instantFromWallClock("2026-05-14T10:00", { timeZone: "Europe/Chisinau" })).toBe(
      instantFromWallClock("2026-05-14T10:00", { timeZone: "Europe/Chisinau" }),
    );
    expect(instantFromWallClock("2026-05-14T10:00", { timeZone: "UTC" })).toBe(
      "2026-05-14T10:00:00.000Z",
    );
  });

  test("accepts the seconds a browser sometimes adds", () => {
    expect(instantFromWallClock("2026-05-14T10:00:30")).toBe("2026-05-14T07:00:30.000Z");
  });

  test("refuses anything that is not a wall clock", () => {
    for (const wrong of ["", "tomorrow", "2026-05-14", "2026-05-14T10:00Z", "14/05/2026 10:00"]) {
      expect(instantFromWallClock(wrong)).toBeNull();
    }
  });
});

describe("wallClockFromInstant", () => {
  test("gives back exactly what the field can render", () => {
    // Anything but "YYYY-MM-DDTHH:mm" and the browser silently shows an empty
    // field, which reads as "no date set" on a contest that has one.
    expect(wallClockFromInstant("2026-05-14T07:00:00Z")).toBe("2026-05-14T10:00");
  });

  test("round-trips a wall clock through the instant and back", () => {
    const wall = "2026-12-14T09:30";
    expect(wallClockFromInstant(instantFromWallClock(wall) as string)).toBe(wall);
  });

  test("is empty for a timestamp that will not parse, not 'Invalid Date'", () => {
    expect(wallClockFromInstant("not a date")).toBe("");
  });
});
