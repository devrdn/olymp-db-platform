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
      { lang: "en", title: "ok", story: "ok", questionsMissing: 0, questionsNotStarted: false },
      { lang: "ru", title: "missing", story: "ok", questionsMissing: 0, questionsNotStarted: false },
    ]);
  });

  test("counts the questions a language has no text for", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [
          { code: "missing_question_translation", lang: "ro", question_id: "q1" },
          { code: "missing_question_translation", lang: "ro", question_id: "q2" },
        ],
      },
      ["en", "ro"],
    );

    expect(gate.byLanguage[1].questionsMissing).toBe(2);
    expect(gate.byLanguage[0].questionsMissing).toBe(0);
  });
});

/**
 * The trap this pair of tests exists for. The gate reports "there is no story"
 * once, without naming a language, and then says nothing further about any
 * language's story — there is nothing to say. A matrix built only from
 * per-language problems reads that silence as approval, and prints a tick
 * beside every language in the same panel that says the story is missing.
 */
describe("summarisePublishCheck, what does not exist yet", () => {
  test("does not call a language's story done when there is no story at all", () => {
    const gate = summarisePublishCheck(
      { ready: false, problems: [{ code: "no_story" }] },
      ["en", "ro"],
    );

    expect(gate.byLanguage.map((row) => row.story)).toEqual(["not-started", "not-started"]);
  });

  test("does not call a language's questions done when there are no questions", () => {
    const gate = summarisePublishCheck(
      { ready: false, problems: [{ code: "no_questions" }] },
      ["en"],
    );

    expect(gate.byLanguage[0].questionsNotStarted).toBe(true);
    expect(gate.byLanguage[0].questionsMissing).toBe(0);
  });

  test("goes back to per-language reporting once the story exists", () => {
    const gate = summarisePublishCheck(
      { ready: false, problems: [{ code: "missing_story_translation", lang: "ro" }] },
      ["en", "ro"],
    );

    expect(gate.byLanguage.map((row) => row.story)).toEqual(["ok", "missing"]);
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

  test("has no matrix at all for a contest that declares no language", () => {
    const gate = summarisePublishCheck({ ready: false, problems: [{ code: "no_languages" }] }, []);

    expect(gate.byLanguage).toEqual([]);
    expect(gate.global).toEqual([{ code: "no_languages", detail: undefined }]);
  });
});
