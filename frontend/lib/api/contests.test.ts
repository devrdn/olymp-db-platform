import { describe, expect, test } from "vitest";

import {
  contentEditable,
  contestCoverSchema,
  contestListSchema,
  contestSchema,
  coverHref,
  defaultLanguage,
  icpcPenaltyFromForm,
  NEXT_STATUSES,
  sequentialActive,
  settingsEditable,
  shapeEditable,
  shapeFromForm,
  titleIn,
  type Contest,
} from "./contests";

function shapeForm(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

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

  test("carries the cover a contest wears", () => {
    const payload = {
      items: [
        {
          id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
          status: "running",
          enrollment: "open",
          question_mode: "multi",
          lang: "ru",
          title: "Ночь в архиве",
          scoring: "points",
          icpc_penalty_min: 20,
          cover_hash: "9f86d081884c7d65",
          cover_attribution: "Photo: A. Organiser, CC BY 4.0",
        },
      ],
      total: 1,
    };

    expect(contestListSchema.parse(payload).items[0]).toMatchObject({
      coverHash: "9f86d081884c7d65",
      coverAttribution: "Photo: A. Organiser, CC BY 4.0",
    });
  });

  // A drawn cover omits the fields, and so does an older server.
  test("reads a listing with no cover fields as a contest with no picture", () => {
    const payload = {
      items: [
        {
          id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
          status: "running",
          enrollment: "open",
          question_mode: "multi",
          lang: "ru",
          title: "Ночь в архиве",
          scoring: "points",
          icpc_penalty_min: 20,
        },
      ],
      total: 1,
    };

    expect(contestListSchema.parse(payload).items[0]).toMatchObject({
      coverHash: "",
      coverAttribution: "",
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

  test("reads whether the viewer may monitor the contest", () => {
    expect(contestSchema.parse({ ...detail, may_monitor: true }).mayMonitor).toBe(true);
    expect(contestSchema.parse(detail).mayMonitor).toBe(false);
  });

  // Go writes `null` for fixed timing; left as is, a number input would show "null".
  test("reads a null duration as an absent one", () => {
    const parsed = contestSchema.parse({ ...detail, timing: "fixed", duration_min: null });

    expect(parsed.durationMin).toBeUndefined();
  });

  test("refuses a status the interface has no screen for", () => {
    expect(() => contestSchema.parse({ ...detail, status: "cancelled" })).toThrow();
  });

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

describe("sequentialActive", () => {
  test("is true only under sequential progression and multi question mode", () => {
    expect(sequentialActive({ progression: "sequential", questionMode: "multi" })).toBe(true);
    expect(sequentialActive({ progression: "sequential", questionMode: "single" })).toBe(false);
    expect(sequentialActive({ progression: "free", questionMode: "multi" })).toBe(false);
    expect(sequentialActive({ progression: "free", questionMode: "single" })).toBe(false);
  });
});

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

// A running contest disables the shape fieldset, so its fields are absent, and
// a defaulted one would make the API refuse the whole PATCH.
describe("shapeFromForm", () => {
  test("sends nothing at all when the whole shape is locked", () => {
    expect(shapeFromForm(shapeForm({}))).toEqual({ ok: true, value: {} });
  });

  test("sends every field when the shape is open, duration included for individual timing", () => {
    expect(
      shapeFromForm(
        shapeForm({
          questionMode: "single",
          progression: "sequential",
          scoring: "icpc",
          timing: "individual",
          durationMin: "45",
        }),
      ),
    ).toEqual({
      ok: true,
      value: {
        question_mode: "single",
        progression: "sequential",
        scoring: "icpc",
        timing: "individual",
        duration_min: 45,
      },
    });
  });

  test("sends a null duration for fixed timing, never leaving it unset", () => {
    expect(
      shapeFromForm(shapeForm({ questionMode: "multi", progression: "free", scoring: "points", timing: "fixed" })),
    ).toEqual({
      ok: true,
      value: { question_mode: "multi", progression: "free", scoring: "points", timing: "fixed", duration_min: null },
    });
  });

  test("refuses individual timing with no valid duration, only while timing was actually submitted", () => {
    expect(shapeFromForm(shapeForm({ timing: "individual" }))).toEqual({ ok: false });
    expect(shapeFromForm(shapeForm({ timing: "individual", durationMin: "0" }))).toEqual({ ok: false });
    expect(shapeFromForm(shapeForm({ timing: "individual", durationMin: "many" }))).toEqual({ ok: false });
  });

  test("never validates a duration when the shape is locked, whatever a stray field carries", () => {
    expect(shapeFromForm(shapeForm({ durationMin: "not a number" }))).toEqual({ ok: true, value: {} });
  });

  test("sends only the fields the form actually carried, never inventing the others", () => {
    expect(shapeFromForm(shapeForm({ scoring: "winner" }))).toEqual({
      ok: true,
      value: { scoring: "winner" },
    });
  });

  test("ignores a value outside the closed set, the same as an absent field", () => {
    expect(shapeFromForm(shapeForm({ questionMode: "essay" }))).toEqual({ ok: true, value: {} });
  });
});

describe("coverHref", () => {
  const contestId = "f767af3b-f135-40d2-a3a6-82d368de1004";
  const hash = "9f2c1ab4d5e6f70819a2b3c4d5e6f7081920a2b3c4d5e6f70819a2b3c4d5e6f7";

  test("carries the hash, so a replaced cover is a different address", () => {
    expect(coverHref(contestId, hash)).toBe(
      `/api/v1/public/contests/${contestId}/cover?size=800&v=${hash}`,
    );
  });

  test("asks for the card's rendition unless the caller wants the large one", () => {
    expect(coverHref(contestId, hash)).toContain("size=800");
    expect(coverHref(contestId, hash, 1600)).toContain("size=1600");
  });
});

describe("contestCoverSchema", () => {
  test("reads what the upload answered with", () => {
    expect(
      contestCoverSchema.parse({
        hash: "abc",
        attribution: "Photo: A. Organiser, CC BY 4.0",
        width: 1600,
        height: 900,
      }),
    ).toEqual({
      hash: "abc",
      attribution: "Photo: A. Organiser, CC BY 4.0",
      width: 1600,
      height: 900,
    });
  });
});
