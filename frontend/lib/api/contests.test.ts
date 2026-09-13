import { describe, expect, test } from "vitest";

import {
  contentEditable,
  contestListSchema,
  contestSchema,
  defaultLanguage,
  icpcPenaltyFromForm,
  NEXT_STATUSES,
  sequentialActive,
  settingsEditable,
  shapeEditable,
  titleIn,
  type Contest,
} from "./contests";

describe("contestListSchema", () => {
  test("parses the listing the API actually returns", () => {
    const payload = {
      items: [
        {
          id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
          status: "running",
          enrollment: "open",
          question_mode: "multi",
          lang: "ru",
          title: "Ночь в архиве",
          description: "Опись пропала между полуночью и рассветом.",
          starts_at: "2026-11-08T19:00:00Z",
          ends_at: "2026-11-08T21:00:00Z",
          scoring: "points",
          icpc_penalty_min: 20,
        },
      ],
      total: 1,
    };

    const parsed = contestListSchema.parse(payload);

    expect(parsed.total).toBe(1);
    expect(parsed.items[0]).toMatchObject({
      status: "running",
      questionMode: "multi",
      title: "Ночь в архиве",
      scoring: "points",
      icpcPenaltyMin: 20,
    });
  });
});

const detail = {
  id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
  status: "draft",
  enrollment: "invite_only",
  question_mode: "single",
  progression: "free",
  scoring: "points",
  icpc_penalty_min: 20,
  timing: "individual",
  duration_min: 90,
  starts_at: "2026-11-08T19:00:00Z",
  ends_at: "2026-11-08T21:00:00Z",
  allowed_cidrs: ["10.24.0.0/16"],
  leaderboard: { freeze_min: 30, names: "full_name", revealed_at: "2026-11-09T10:00:00Z" },
  settings: { query_rate_limit_per_min: 30, grace_period_min: 5 },
  languages: [
    { code: "en", is_default: false },
    { code: "ro", is_default: true },
  ],
  translations: {
    en: { title: "Night in the archive" },
    ro: { title: "Noapte în arhivă", description: "Inventarul a dispărut." },
  },
  created_at: "2026-10-01T08:00:00Z",
  updated_at: "2026-10-02T08:00:00Z",
};

describe("contestSchema", () => {
  test("parses the staff view, every translation included", () => {
    const parsed = contestSchema.parse(detail);

    expect(parsed).toMatchObject({
      questionMode: "single",
      progression: "free",
      scoring: "points",
      timing: "individual",
      durationMin: 90,
      allowedCidrs: ["10.24.0.0/16"],
    });
    expect(Object.keys(parsed.translations)).toEqual(["en", "ro"]);
    expect(parsed.settings.queryRateLimitPerMin).toBe(30);
  });

  /**
   * Go writes `duration_min: null` for a contest on the fixed timing model,
   * because the field is a pointer. Left as `null` it would reach a number
   * input as the string "null".
   */
  test("reads how the leaderboard is frozen, labelled and revealed", () => {
    expect(contestSchema.parse(detail).leaderboard).toEqual({
      freezeMin: 30,
      names: "full_name",
      revealedAt: "2026-11-09T10:00:00Z",
    });
    expect(
      contestSchema.parse({ ...detail, leaderboard: { freeze_min: null, names: "login" } }).leaderboard,
    ).toEqual({ freezeMin: null, names: "login", revealedAt: undefined });
  });

  test("reads a null duration as an absent one", () => {
    const parsed = contestSchema.parse({ ...detail, timing: "fixed", duration_min: null });

    expect(parsed.durationMin).toBeUndefined();
  });

  test("refuses a status the interface has no screen for", () => {
    expect(() => contestSchema.parse({ ...detail, status: "cancelled" })).toThrow();
  });

  // The ICPC penalty travels alongside scoring rather than nested under
  // `leaderboard` — it is a property of the contest, not of the table — and
  // is present whatever the scoring mode is, since the mode can still be
  // reverted before the contest starts.
  test("accepts ICPC scoring and reads its own penalty", () => {
    const parsed = contestSchema.parse({ ...detail, scoring: "icpc", icpc_penalty_min: 15 });

    expect(parsed.scoring).toBe("icpc");
    expect(parsed.icpcPenaltyMin).toBe(15);
  });
});

describe("titleIn", () => {
  const contest = contestSchema.parse(detail) as Contest;

  test("gives the title in the language being read", () => {
    expect(titleIn(contest, "en")).toBe("Night in the archive");
  });

  /**
   * An editing screen with a blank heading tells the author nothing about
   * which contest they have open. A listing must not do this — which is why
   * the server negotiates that one instead of this function.
   */
  test("falls through to the contest's default when the read language has none", () => {
    expect(titleIn(contest, "ru")).toBe("Noapte în arhivă");
  });

  test("falls through to any language at all rather than showing nothing", () => {
    const onlyEnglish = contestSchema.parse({
      ...detail,
      languages: [{ code: "ro", is_default: true }],
      translations: { en: { title: "Night in the archive" } },
    });

    expect(titleIn(onlyEnglish, "ro")).toBe("Night in the archive");
  });

  test("returns an empty string when a draft has no title yet", () => {
    const untitled = contestSchema.parse({ ...detail, translations: {} });

    expect(titleIn(untitled, "en")).toBe("");
  });
});

describe("defaultLanguage", () => {
  test("is the one the contest marked", () => {
    expect(defaultLanguage(contestSchema.parse(detail))).toBe("ro");
  });

  test("falls back to the first declared when none is marked", () => {
    const unmarked = contestSchema.parse({
      ...detail,
      languages: [
        { code: "en", is_default: false },
        { code: "ro", is_default: false },
      ],
    });

    expect(defaultLanguage(unmarked)).toBe("en");
  });

  test("is absent on a contest that declares no language yet", () => {
    expect(defaultLanguage(contestSchema.parse({ ...detail, languages: [] }))).toBeUndefined();
  });
});

/**
 * Two lines, not one, and the gap between them is the point. Settings stay
 * open while a contest runs — extending the window after a power cut is
 * exactly what a running contest needs — while the content and the shape
 * freeze at the start, because people are already answering under them.
 */
describe("the editing windows", () => {
  test("content closes when the contest starts", () => {
    expect(contentEditable("draft")).toBe(true);
    expect(contentEditable("published")).toBe(true);
    expect(contentEditable("running")).toBe(false);
    expect(contentEditable("finished")).toBe(false);
  });

  test("settings stay open through a running contest", () => {
    expect(settingsEditable("running")).toBe(true);
    expect(settingsEditable("finished")).toBe(false);
  });

  test("the shape closes with the content, not with the settings", () => {
    expect(shapeEditable("published")).toBe(true);
    expect(shapeEditable("running")).toBe(false);
    expect(settingsEditable("running")).toBe(true);
  });
});

describe("NEXT_STATUSES", () => {
  test("lets a published contest be pulled back to draft", () => {
    // Publishing is how an author finds out the gate passes. Undoing it must
    // not require deleting the contest.
    expect(NEXT_STATUSES.published).toContain("draft");
  });

  test("offers no way out of an archived contest", () => {
    expect(NEXT_STATUSES.archived).toHaveLength(0);
  });

  test("never offers a jump that skips running", () => {
    expect(NEXT_STATUSES.draft).not.toContain("running");
    expect(NEXT_STATUSES.published).not.toContain("finished");
  });
});

// Finding 4: the question editor and the settings panel must read this rule
// from the one place the Go side also reads it from (contests.Contest's own
// SequentialActive), not repeat the two-field comparison themselves.
describe("sequentialActive", () => {
  test("is true only under sequential progression and multi question mode", () => {
    expect(sequentialActive({ progression: "sequential", questionMode: "multi" })).toBe(true);
    expect(sequentialActive({ progression: "sequential", questionMode: "single" })).toBe(false);
    expect(sequentialActive({ progression: "free", questionMode: "multi" })).toBe(false);
    expect(sequentialActive({ progression: "free", questionMode: "single" })).toBe(false);
  });
});

/**
 * The ICPC penalty is locked with the rest of the shape once the contest
 * starts (`shapeEditable`), the same as the scoring radio it sits beside — a
 * disabled field submits nothing, and that has to read as "leave it alone",
 * never as "clear it" (there is no cleared state for a penalty in this mode).
 */
describe("icpcPenaltyFromForm", () => {
  test("reads a locked field as 'send no key'", () => {
    expect(icpcPenaltyFromForm(null)).toEqual({ ok: true, value: undefined });
  });

  test("reads a configured penalty, zero included", () => {
    expect(icpcPenaltyFromForm("0")).toEqual({ ok: true, value: 0 });
    expect(icpcPenaltyFromForm("20")).toEqual({ ok: true, value: 20 });
    expect(icpcPenaltyFromForm("240")).toEqual({ ok: true, value: 240 });
  });

  test("refuses an amount outside 0 to 240, rather than clamping it", () => {
    expect(icpcPenaltyFromForm("241")).toEqual({ ok: false });
    expect(icpcPenaltyFromForm("-1")).toEqual({ ok: false });
  });

  test("refuses anything that is not a whole number, rather than rounding it", () => {
    expect(icpcPenaltyFromForm("20.5")).toEqual({ ok: false });
    expect(icpcPenaltyFromForm("many")).toEqual({ ok: false });
    expect(icpcPenaltyFromForm("")).toEqual({ ok: false });
  });
});
