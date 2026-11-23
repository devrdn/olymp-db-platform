import { describe, expect, test } from "vitest";

import { freezeForForm, freezeFromForm, identityHue } from "./leaderboard-terms";

describe("a participant's colour", () => {
  test("is one of the six identity hues, and the same for the same label every time", () => {
    const hue = identityHue("ivanov");
    expect(hue).toBeGreaterThanOrEqual(1);
    expect(hue).toBeLessThanOrEqual(6);
    expect(identityHue("ivanov")).toBe(hue);
  });

  test("spreads a roster across the palette rather than painting it one colour", () => {
    const hues = new Set(
      Array.from({ length: 40 }, (_, i) => identityHue(`student${i}`)),
    );
    expect(hues.size).toBe(6);
  });
});

describe("the freeze an organiser typed", () => {
  test("no freeze clears it, and a locked field says nothing at all", () => {
    expect(freezeFromForm("none", "", "minutes")).toEqual({
      ok: true,
      value: null,
    });
    expect(freezeFromForm(null, null, null)).toEqual({
      ok: true,
      value: undefined,
    });
  });

  test("hours become minutes", () => {
    expect(freezeFromForm("before", "2", "hours")).toEqual({
      ok: true,
      value: 120,
    });
    expect(freezeFromForm("before", "45", "minutes")).toEqual({
      ok: true,
      value: 45,
    });
  });

  test("a freeze that is not a whole, positive number of minutes is refused rather than rounded", () => {
    for (const amount of ["", "0", "-3", "1.5", "abc"]) {
      expect(freezeFromForm("before", amount, "minutes").ok).toBe(false);
    }
  });

  test("an amount shown back is in hours when it divides into hours", () => {
    expect(freezeForForm(120)).toEqual({
      mode: "before",
      amount: 2,
      unit: "hours",
    });
    expect(freezeForForm(90)).toEqual({
      mode: "before",
      amount: 90,
      unit: "minutes",
    });
    expect(freezeForForm(null)).toEqual({
      mode: "none",
      amount: 30,
      unit: "minutes",
    });
  });
});
