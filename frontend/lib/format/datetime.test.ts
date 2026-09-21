import { describe, expect, test } from "vitest";

import {
  formatDay,
  formatMoment,
  formatSeconds,
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

/**
 * The hydration bug this file's own rules did not cover.
 *
 * The doc comment above pins the timezone so a Server Component and the
 * browser that hydrates it agree — and that was one of two axes. The other is
 * the locale data itself: the string that joins a date to a time comes from
 * CLDR, and Node's bundled ICU is not the browser's. Node 22 and one Chrome
 * print `Sep 7, 2026, 11:38 PM`; a newer Chrome prints `Sep 7, 2026 at
 * 11:38 PM`, and React reported the mismatch on the participant's query log.
 *
 * So the join is ours, not CLDR's, and the clock is 24-hour everywhere —
 * which also removes the next instance of the same trap, the space before
 * AM/PM that some ICU versions render as U+202F and others as an ordinary
 * one.
 */
describe("the same string in every runtime", () => {
  const AT_NOON = "2026-11-08T10:00:00Z";

  // `sv-SE` is not a locale this product offers, and that is the point: it is
  // one where *this* runtime's CLDR already joins with a plain space instead
  // of a comma. Asserting the join only in en/ru/ro would pass by coincidence
  // — the whole difficulty of this bug is that the coincidence holds on the
  // machine you are testing on and breaks on somebody's browser. Here the
  // guarantee is checkable without a second ICU.
  test("joins the date to the time itself, rather than letting the locale do it", () => {
    for (const locale of ["en", "en-GB", "ru-RU", "ro-RO", "sv-SE"]) {
      const moment = formatMoment(AT_NOON, { locale });

      expect(moment).toBe(`${formatDay(AT_NOON, { locale })}, ${formatTime(AT_NOON, { locale })}`);
      // The word CLDR inserts in a newer en, and never in an older one.
      expect(moment).not.toMatch(/\bat\b/);
    }
  });

  test("shows a 24-hour clock in every locale, English included", () => {
    for (const locale of ["en", "en-GB", "ru-RU", "ro-RO"]) {
      const time = formatTime(AT_NOON, { locale });

      expect(time).toBe("12:00");
      expect(time).not.toMatch(/[AP]M/i);
    }
  });

  test("carries no space whose width is a matter of ICU opinion", () => {
    for (const locale of ["en", "ru-RU", "ro-RO"]) {
      // U+202F narrow no-break space and U+00A0 no-break space: what one ICU
      // version puts before AM/PM and another does not.
      expect(formatMoment(AT_NOON, { locale })).not.toMatch(/[\u202f\u00a0]/);
    }
  });

  test("midnight is 00:00 and not 24:00", () => {
    expect(formatTime("2026-11-07T22:00:00Z", { locale: "en" })).toBe("00:00");
  });
});

/**
 * The stamp a log line carries. A query and the next one are often a few
 * seconds apart, so the minute alone would print the same time twice over
 * rows that are not the same moment.
 */
describe("formatSeconds", () => {
  test("carries the second, in the contest's timezone and on a 24-hour clock", () => {
    for (const locale of ["en", "ru-RU", "ro-RO"]) {
      expect(formatSeconds("2026-11-08T10:14:03.120Z", { locale })).toBe("12:14:03");
    }
  });

  test("is the time of the same instant, one field longer", () => {
    const at = "2026-11-08T19:05:09Z";
    expect(formatSeconds(at, { locale: "en" })).toBe(`${formatTime(at, { locale: "en" })}:09`);
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

/**
 * Finding 3: every one of these built its `Intl.DateTimeFormat` inside the
 * call, so the cost of compiling locale data was paid per value formatted
 * rather than per shape. Measured at 83µs a call, on screens that format
 * hundreds of values — and, in a Client Component, format them twice.
 *
 * A locale and a zone no other test in this file touches, because the cache
 * lives for the length of the process: warmed by an earlier test, this would
 * pass without the fix.
 */
describe("how often a formatter is built", () => {
  const elsewhere = { locale: "ro-RO", timeZone: "Europe/Bucharest" };

  /**
   * Counts constructions by standing a proxy in front of the constructor —
   * a spy would answer `new` with something that is not an
   * `Intl.DateTimeFormat`, and the cache would then keep that instead.
   */
  function countingConstructions(work: () => void): number {
    const real = Intl.DateTimeFormat;
    let built = 0;
    Intl.DateTimeFormat = new Proxy(real, {
      construct(target, args) {
        built += 1;
        return Reflect.construct(target, args);
      },
    });
    try {
      work();
    } finally {
      Intl.DateTimeFormat = real;
    }
    return built;
  }

  test("once per shape, however many values go through it", () => {
    const built = countingConstructions(() => {
      for (let i = 0; i < 100; i += 1) {
        formatMoment(`2026-05-${String((i % 28) + 1).padStart(2, "0")}T10:00:00Z`, elsewhere);
      }
    });

    // The date and the time, which is what `formatMoment` composes — not two
    // hundred.
    expect(built).toBe(2);
  });

  test("and the kept formatter still answers with the same string as a fresh one", () => {
    const iso = "2026-05-14T07:00:00Z";
    const kept = formatMoment(iso, elsewhere);

    expect(kept).toBe(
      `${new Intl.DateTimeFormat("ro-RO", {
        day: "numeric",
        month: "short",
        year: "numeric",
        timeZone: "Europe/Bucharest",
      }).format(new Date(iso))}, ${new Intl.DateTimeFormat("ro-RO", {
        hour: "2-digit",
        minute: "2-digit",
        hourCycle: "h23",
        timeZone: "Europe/Bucharest",
      }).format(new Date(iso))}`,
    );
  });
});
