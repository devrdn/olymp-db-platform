import { describe, expect, test } from "vitest";

import { freezeForForm, freezeFromForm, identityHue, staffStandingsSchema, standingsSchema } from "./leaderboard";

describe("the standings a viewer is sent", () => {
  test("reads a frozen table and keeps an unplaced row unplaced", () => {
    const parsed = standingsSchema.parse({
      state: "frozen",
      scoring: "winner",
      title: "The Library Murder",
      frozen_at: "2026-09-20T11:30:00Z",
      generated_at: "2026-09-20T12:04:10Z",
      truncated: false,
      rows: [
        { place: 1, label: "ivanov", points: 4, solved: 1, winner: true, last_scored_at: "2026-09-20T11:12:03Z" },
        { place: null, label: "", deleted: true, points: 9, solved: 2 },
        { place: null, label: "me", points: 0, solved: 0, is_you: true },
      ],
    });

    expect(parsed.state).toBe("frozen");
    expect(parsed.frozenAt).toBe("2026-09-20T11:30:00Z");
    expect(parsed.rows[0]).toMatchObject({ place: 1, winner: true, lastScoredAt: "2026-09-20T11:12:03Z" });
    expect(parsed.rows[1]).toMatchObject({ place: null, deleted: true, winner: false, isYou: false });
    expect(parsed.rows[2].isYou).toBe(true);
  });

  test("refuses a state this build does not know rather than guessing at it", () => {
    expect(() =>
      standingsSchema.parse({ state: "thawing", scoring: "points", title: "", generated_at: "x", truncated: false, rows: [] }),
    ).toThrow();
  });

  test("reads the staff table with both names and what everybody else sees", () => {
    const parsed = staffStandingsSchema.parse({
      shown: { state: "frozen", frozen_at: "2026-09-20T11:30:00Z" },
      scoring: "points",
      freeze_min: 30,
      names: "login",
      generated_at: "2026-09-20T12:04:10Z",
      truncated: false,
      rows: [{ place: 1, login: "ivanov", full_name: "Ivan Ivanov", disqualified: true, points: 3, solved: 1 }],
    });

    expect(parsed.shown).toEqual({ state: "frozen", frozenAt: "2026-09-20T11:30:00Z" });
    expect(parsed.freezeMin).toBe(30);
    expect(parsed.revealedAt).toBeUndefined();
    expect(parsed.rows[0]).toMatchObject({ login: "ivanov", fullName: "Ivan Ivanov", disqualified: true });
  });
});

describe("a participant's colour", () => {
  test("is one of the six identity hues, and the same for the same label every time", () => {
    const hue = identityHue("ivanov");
    expect(hue).toBeGreaterThanOrEqual(1);
    expect(hue).toBeLessThanOrEqual(6);
    expect(identityHue("ivanov")).toBe(hue);
  });

  test("spreads a roster across the palette rather than painting it one colour", () => {
    const hues = new Set(Array.from({ length: 40 }, (_, i) => identityHue(`student${i}`)));
    expect(hues.size).toBe(6);
  });
});

describe("the freeze an organiser typed", () => {
  test("no freeze clears it, and a locked field says nothing at all", () => {
    expect(freezeFromForm("none", "", "minutes")).toEqual({ ok: true, value: null });
    expect(freezeFromForm(null, null, null)).toEqual({ ok: true, value: undefined });
  });

  test("hours become minutes", () => {
    expect(freezeFromForm("before", "2", "hours")).toEqual({ ok: true, value: 120 });
    expect(freezeFromForm("before", "45", "minutes")).toEqual({ ok: true, value: 45 });
  });

  test("a freeze that is not a whole, positive number of minutes is refused rather than rounded", () => {
    for (const amount of ["", "0", "-3", "1.5", "abc"]) {
      expect(freezeFromForm("before", amount, "minutes").ok).toBe(false);
    }
  });

  test("an amount shown back is in hours when it divides into hours", () => {
    expect(freezeForForm(120)).toEqual({ mode: "before", amount: 2, unit: "hours" });
    expect(freezeForForm(90)).toEqual({ mode: "before", amount: 90, unit: "minutes" });
    expect(freezeForForm(null)).toEqual({ mode: "none", amount: 30, unit: "minutes" });
  });
});
