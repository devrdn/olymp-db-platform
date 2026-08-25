import { describe, expect, test } from "vitest";

import { formatMoment } from "./datetime";

describe("formatMoment", () => {
  test("renders a UTC instant in the contest's timezone, not the machine's", () => {
    // 19:00 UTC on 8 November is 21:00 in Chisinau.
    expect(formatMoment("2026-11-08T19:00:00Z", { timeZone: "Europe/Chisinau" })).toBe(
      "8 нояб. 2026 г., 21:00",
    );
  });
});
