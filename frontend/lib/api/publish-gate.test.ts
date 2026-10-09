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

// "No story" arrives once without a language; it must not read as every
// language being done.
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
      { code: "no_schedule", count: 1 },
      { code: "single_mode_needs_one_question", count: 1, detail: "3" },
    ]);
    expect(gate.byLanguage).toHaveLength(1);
  });

  test("has no matrix at all for a contest that declares no language", () => {
    const gate = summarisePublishCheck({ ready: false, problems: [{ code: "no_languages" }] }, []);

    expect(gate.byLanguage).toEqual([]);
    expect(gate.global).toEqual([{ code: "no_languages", count: 1 }]);
  });
});

describe("summarisePublishCheck, problems that repeat", () => {
  test("collapses a repeated code into one entry that counts itself", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [
          { code: "no_reference_answer", question_id: "q1" },
          { code: "no_reference_answer", question_id: "q2" },
          { code: "no_schedule" },
        ],
      },
      ["en"],
    );

    expect(gate.global).toEqual([
      { code: "no_reference_answer", count: 2 },
      { code: "no_schedule", count: 1 },
    ]);
  });

  test("keeps the server's detail on a problem that occurs once", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [{ code: "single_mode_needs_one_question", detail: "3 questions" }],
      },
      ["en"],
    );

    expect(gate.global).toEqual([
      { code: "single_mode_needs_one_question", count: 1, detail: "3 questions" },
    ]);
  });

  test("counts an unlabelled choice as a question that is not finished", () => {
    const gate = summarisePublishCheck(
      {
        ready: false,
        problems: [{ code: "missing_choice_label", lang: "ro", question_id: "q1", detail: "a" }],
      },
      ["en", "ro"],
    );

    expect(gate.byLanguage).toEqual([
      { lang: "en", title: "ok", story: "ok", questionsMissing: 0, questionsNotStarted: false },
      { lang: "ro", title: "ok", story: "ok", questionsMissing: 1, questionsNotStarted: false },
    ]);
  });
});
