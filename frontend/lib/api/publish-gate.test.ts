import { describe, expect, test } from "vitest";

import { summarisePublishCheck } from "./publish-gate";

describe("summarisePublishCheck", () => {
  test("marks a language complete when no problem names it", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [{ code: "missing_contest_translation", lang: "ru" }],
      },
      ["en", "ru"],
    );

    expect(gate.byLanguage).toEqual([
      { lang: "en", title: "ok", story: "ok", questionsMissing: 0 },
      { lang: "ru", title: "missing", story: "ok", questionsMissing: 0 },
    ]);
  });
});

describe("summarisePublishCheck, contest-wide problems", () => {
  test("reports problems that name no language separately from the matrix", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [
          { code: "no_schedule" },
          { code: "single_mode_needs_one_question", detail: "3" },
          { code: "missing_contest_translation", lang: "ru" },
        ],
      },
      ["ru"],
    );

    expect(gate.global).toEqual([
      { code: "no_schedule", detail: undefined },
      { code: "single_mode_needs_one_question", detail: "3" },
    ]);
    expect(gate.byLanguage).toHaveLength(1);
  });
});
