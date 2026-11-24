import { describe, expect, test } from "vitest";

import {
  archiveSettingsFromForm,
  contestCoverSchema,
  contestListSchema,
  contestSchema,
  defaultLanguage,
  graceFromForm,
  icpcPenaltyFromForm,
  NEXT_STATUSES,
  prizePlacesFromForm,
  sequentialActive,
  shapeFromForm,
  titleIn,
  type Contest,
} from "./contests";
import { MAX_GRACE_PERIOD_MIN } from "./contests-terms";

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

  // The play screen offers the database reset only where writing is allowed.
  test("carries the mode of the contest's SQL policy", () => {
    const item = {
      id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      status: "running",
      enrollment: "open",
      question_mode: "multi",
      lang: "ru",
      title: "Ночь в архиве",
      scoring: "points",
      icpc_penalty_min: 20,
    };

    expect(contestListSchema.parse({ items: [{ ...item, sql_mode: "read_write" }], total: 1 }).items[0].sqlMode).toBe(
      "read_write",
    );
    // An older server omits it; nothing is offered that it would refuse.
    expect(contestListSchema.parse({ items: [item], total: 1 }).items[0].sqlMode).toBe("read_only");
  });

  test("carries whether the console offers its result as a CSV file", () => {
    const item = {
      id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      status: "running",
      enrollment: "open",
      question_mode: "multi",
      lang: "ru",
      title: "Ночь в архиве",
      scoring: "points",
      icpc_penalty_min: 20,
    };

    expect(
      contestListSchema.parse({ items: [{ ...item, result_download: false }], total: 1 }).items[0].resultDownload,
    ).toBe(false);
    // An older server omits it, and offered the download to everybody.
    expect(contestListSchema.parse({ items: [item], total: 1 }).items[0].resultDownload).toBe(true);
  });

  test("carries whether the schema panel offers the diagram", () => {
    const item = {
      id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      status: "running",
      enrollment: "open",
      question_mode: "multi",
      lang: "ru",
      title: "Ночь в архиве",
      scoring: "points",
      icpc_penalty_min: 20,
    };

    expect(
      contestListSchema.parse({ items: [{ ...item, schema_diagram: false }], total: 1 }).items[0].schemaDiagram,
    ).toBe(false);
    // An older server omits it, and the diagram was there for everybody.
    expect(contestListSchema.parse({ items: [item], total: 1 }).items[0].schemaDiagram).toBe(true);
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
  result_download: true,
  schema_diagram: true,
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
  version: 4,
};

describe("contestSchema, the trusted-domain limit", () => {
  test("reads trusted_domain_only into the settings", () => {
    const parsed = contestSchema.parse({ ...detail, settings: { ...detail.settings, trusted_domain_only: true } });

    expect(parsed.settings.trustedDomainOnly).toBe(true);
  });

  test("reads it as off when the server leaves it out", () => {
    expect(contestSchema.parse(detail).settings.trustedDomainOnly).toBe(false);
  });
});

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

  // A form sends it back, so a save made after the row moved is refused.
  test("reads the version the row was at", () => {
    expect(contestSchema.parse(detail).version).toBe(4);
  });

  test.each([
    ["without a version", undefined],
    ["with a negative one", -1],
    ["with a fractional one", 1.5],
    ["with one past what a number holds exactly", Number.MAX_SAFE_INTEGER + 1],
  ])("refuses an answer %s, which no settings form could send back", (_, version) => {
    expect(contestSchema.safeParse({ ...detail, version }).success).toBe(false);
  });

  test("reads a contest nobody has written since its creation, at version 0", () => {
    expect(contestSchema.parse({ ...detail, version: 0 }).version).toBe(0);
  });

  test("reads whether the viewer may monitor the contest", () => {
    expect(contestSchema.parse({ ...detail, may_monitor: true }).mayMonitor).toBe(true);
    expect(contestSchema.parse(detail).mayMonitor).toBe(false);
  });

  test("reads whether the viewer may answer the contest's questions to the organisers", () => {
    expect(contestSchema.parse({ ...detail, may_clarify: true }).mayClarify).toBe(true);
    expect(contestSchema.parse(detail).mayClarify).toBe(false);
  });

  test("reads the prize places, and none from a server older than them", () => {
    expect(contestSchema.parse({ ...detail, prize_places: { first: 1, second: 2, third: 3 } }).prizePlaces).toEqual({
      first: 1,
      second: 2,
      third: 3,
    });
    expect(contestSchema.parse(detail).prizePlaces).toEqual({ first: 0, second: 0, third: 0 });
  });

  test("reads whether the console offers its result as a CSV file", () => {
    expect(contestSchema.parse(detail).resultDownload).toBe(true);
    expect(contestSchema.parse({ ...detail, result_download: false }).resultDownload).toBe(false);
    expect(contestSchema.parse({ ...detail, result_download: undefined }).resultDownload).toBe(true);
  });

  test("reads whether the schema panel offers the diagram", () => {
    expect(contestSchema.parse(detail).schemaDiagram).toBe(true);
    expect(contestSchema.parse({ ...detail, schema_diagram: false }).schemaDiagram).toBe(false);
    expect(contestSchema.parse({ ...detail, schema_diagram: undefined }).schemaDiagram).toBe(true);
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
describe("prizePlacesFromForm", () => {
  function form(fields: Record<string, string>): FormData {
    const data = new FormData();
    for (const [key, value] of Object.entries(fields)) data.set(key, value);
    return data;
  }

  test("reads the three counts, a blank one as none", () => {
    expect(prizePlacesFromForm(form({ prizeFirst: "1", prizeSecond: " 2 ", prizeThird: "" }))).toEqual({
      ok: true,
      value: { first: 1, second: 2, third: 0 },
    });
  });

  test("accepts the bound and refuses anything past it, negative or not whole", () => {
    expect(prizePlacesFromForm(form({ prizeFirst: "1000", prizeSecond: "0", prizeThird: "0" })).ok).toBe(true);
    for (const bad of ["1001", "-1", "1.5", "two", "1e2"]) {
      expect(prizePlacesFromForm(form({ prizeFirst: bad, prizeSecond: "0", prizeThird: "0" }))).toEqual({ ok: false });
    }
  });
});

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

describe("contestSchema, the archive fields", () => {
  test("a payload without them reads as open, tasks hidden", () => {
    const parsed = contestSchema.parse(detail);

    expect(parsed.visibility).toBe("open");
    expect(parsed.archiveTasks).toBe(false);
  });

  test("carries what the server sent", () => {
    const parsed = contestSchema.parse({ ...detail, visibility: "closed", archive_tasks: true });

    expect(parsed.visibility).toBe("closed");
    expect(parsed.archiveTasks).toBe(true);
  });
});

describe("archiveSettingsFromForm", () => {
  test("reads the visibility and the switch, an absent box being off", () => {
    expect(archiveSettingsFromForm(shapeForm({ visibility: "closed", archiveTasks: "on" }))).toEqual({
      ok: true,
      value: { visibility: "closed", archiveTasks: true },
    });
    expect(archiveSettingsFromForm(shapeForm({ visibility: "open" }))).toEqual({
      ok: true,
      value: { visibility: "open", archiveTasks: false },
    });
  });

  test("refuses a visibility outside the list, or none", () => {
    expect(archiveSettingsFromForm(shapeForm({ visibility: "public" }))).toEqual({ ok: false });
    expect(archiveSettingsFromForm(shapeForm({}))).toEqual({ ok: false });
  });
});

describe("graceFromForm", () => {
  const form = (value: string) => {
    const data = new FormData();
    data.set("gracePeriodMin", value);
    return data;
  };

  test("takes a value longer than the one in force", () => {
    expect(graceFromForm(form("4320"), 1440)).toEqual({ ok: true, value: 4320 });
  });

  test("refuses a value that does not exceed the contest's own grace", () => {
    expect(graceFromForm(form("1440"), 1440)).toEqual({ ok: false, code: "grace_not_longer" });
    expect(graceFromForm(form("60"), 1440)).toEqual({ ok: false, code: "grace_not_longer" });
  });

  test("leaves the comparison to the server while the installation default is in force", () => {
    expect(graceFromForm(form("1"), 0)).toEqual({ ok: true, value: 1 });
    expect(graceFromForm(form("60"), 0)).toEqual({ ok: true, value: 60 });
  });

  test("accepts the bound and refuses what is past it", () => {
    expect(graceFromForm(form(String(MAX_GRACE_PERIOD_MIN)), 0)).toEqual({ ok: true, value: 129_600 });
    expect(graceFromForm(form("129601"), 0)).toEqual({ ok: false, code: "invalid_request" });
  });

  test.each(["0", "", "2.5", "-5", "abc"])("refuses %j as invalid", (value) => {
    expect(graceFromForm(form(value), 0)).toEqual({ ok: false, code: "invalid_request" });
  });
});
