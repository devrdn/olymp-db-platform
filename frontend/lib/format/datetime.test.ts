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

describe("formatMoment", () => {
  test("renders a UTC instant in the contest's timezone, not the machine's", () => {
    // 19:00 UTC on 8 November is 21:00 in Chisinau.
    expect(formatMoment("2026-11-08T19:00:00Z", { timeZone: "Europe/Chisinau" })).toBe(
      "8 нояб. 2026 г., 21:00",
    );
  });
});

describe("the same string in every runtime", () => {
  const AT_NOON = "2026-11-08T10:00:00Z";

  // `sv-SE` already joins with a space in this runtime's CLDR, so the test
  // proves the join is ours rather than passing by coincidence.
  test("joins the date to the time itself, rather than letting the locale do it", () => {
    for (const locale of ["en", "en-GB", "ru-RU", "ro-RO", "sv-SE"]) {
      const moment = formatMoment(AT_NOON, { locale });

      expect(moment).toBe(`${formatDay(AT_NOON, { locale })}, ${formatTime(AT_NOON, { locale })}`);
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
      // U+202F and U+00A0: what one ICU version puts before AM/PM and another does not.
      expect(formatMoment(AT_NOON, { locale })).not.toMatch(/[\u202f\u00a0]/);
    }
  });

  test("midnight is 00:00 and not 24:00", () => {
    expect(formatTime("2026-11-07T22:00:00Z", { locale: "en" })).toBe("00:00");
  });
});

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
    // 22:30Z is already the next day in Chisinau (UTC+2).
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

describe("instantFromWallClock", () => {
  test("reads a summer wall clock as the university's summer time", () => {
    // Chisinau is UTC+3 in May.
    expect(instantFromWallClock("2026-05-14T10:00")).toBe("2026-05-14T07:00:00.000Z");
  });

  test("reads a winter wall clock as the university's winter time", () => {
    // UTC+2 in December, so the offset cannot be a constant.
    expect(instantFromWallClock("2026-12-14T10:00")).toBe("2026-12-14T08:00:00.000Z");
  });

  test("does not depend on the zone the process happens to run in", () => {
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

// A locale and zone no other test touches: the cache lives for the process,
// and a warm cache would pass without caching.
describe("how often a formatter is built", () => {
  const elsewhere = { locale: "ro-RO", timeZone: "Europe/Bucharest" };

  // A proxy, not a spy: a spy would answer `new` with something the cache keeps.
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

    // Day and time, which `formatMoment` composes.
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
