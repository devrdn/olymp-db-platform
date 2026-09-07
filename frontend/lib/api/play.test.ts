import { describe, expect, test } from "vitest";

import { answerResultSchema, playQuestionListSchema, playQuestionSchema, playStorySchema } from "./play";

describe("playStorySchema", () => {
  test("parses the negotiated language and the body", () => {
    expect(playStorySchema.parse({ lang: "en", body_md: "The greenhouse was locked." })).toEqual({
      lang: "en",
      bodyMd: "The greenhouse was locked.",
    });
  });
});

describe("playQuestionSchema", () => {
  const base = {
    id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    kind: "choice",
    points: 10,
    choice_ids: ["a", "b"],
    body_md: "Who was in the greenhouse?",
    choices: { a: "The butler", b: "The gardener" },
    closed: false,
    can_answer: true,
  };

  test("reads an omitted attempt count as no limit, not as zero", () => {
    expect(playQuestionSchema.parse(base).attemptsRemaining).toBeUndefined();
  });

  test("keeps zero attempts left distinct from no limit", () => {
    expect(playQuestionSchema.parse({ ...base, attempts_remaining: 0 }).attemptsRemaining).toBe(0);
  });

  test("reads a null attempt count the same as an omitted one", () => {
    expect(playQuestionSchema.parse({ ...base, attempts_remaining: null }).attemptsRemaining).toBeUndefined();
  });

  test("carries can_answer and closed as the server sent them, not derived", () => {
    const parsed = playQuestionSchema.parse({ ...base, closed: true, can_answer: false });
    expect(parsed.closed).toBe(true);
    expect(parsed.canAnswer).toBe(false);
  });

  test("a text question with no choices reads as having none", () => {
    const parsed = playQuestionSchema.parse({
      ...base,
      kind: "text",
      choice_ids: [],
      choices: undefined,
    });
    expect(parsed.choices).toEqual({});
  });

  test("refuses a kind the API contract does not declare", () => {
    expect(() => playQuestionSchema.parse({ ...base, kind: "essay" })).toThrow();
  });
});

describe("playQuestionListSchema", () => {
  test("parses an empty list rather than requiring at least one question", () => {
    expect(playQuestionListSchema.parse({ lang: "en", items: [] })).toEqual({ lang: "en", items: [] });
  });
});

describe("answerResultSchema", () => {
  test("parses a correct answer", () => {
    expect(
      answerResultSchema.parse({ correct: true, points_awarded: 10, attempts_remaining: 2, closed: true }),
    ).toEqual({ correct: true, pointsAwarded: 10, attemptsRemaining: 2, closed: true });
  });

  test("reads an omitted attempts_remaining as no limit", () => {
    expect(
      answerResultSchema.parse({ correct: false, points_awarded: 0, closed: false }).attemptsRemaining,
    ).toBeUndefined();
  });
});
