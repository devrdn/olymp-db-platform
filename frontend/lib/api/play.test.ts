import { describe, expect, test } from "vitest";

import {
  answerResultSchema,
  playQuestionListSchema,
  playQuestionSchema,
  playStorySchema,
  type OpenPlayQuestion,
} from "./play";

describe("playStorySchema", () => {
  test("parses the negotiated language and the body", () => {
    expect(playStorySchema.parse({ lang: "en", body_md: "The greenhouse was locked." })).toEqual({
      lang: "en",
      bodyMd: "The greenhouse was locked.",
      schemaNotesMd: null,
    });
  });

  test("carries the table descriptions the server resolved for this language", () => {
    const parsed = playStorySchema.parse({
      lang: "en",
      body_md: "The greenhouse was locked.",
      schema_notes_md: "## suspects\nOne row per person of interest.",
    });

    expect(parsed.schemaNotesMd).toBe("## suspects\nOne row per person of interest.");
  });

  // Null decides that the Tables tab is not offered at all.
  test.each([
    ["empty", ""],
    ["blank", "  \n\t "],
  ])("reads %s table descriptions as none", (_name, notes) => {
    expect(playStorySchema.parse({ lang: "en", body_md: "x", schema_notes_md: notes }).schemaNotesMd).toBeNull();
  });
});

/** Parses one item through the list's own schema and insists it is an open question. */
function parseOpen(raw: unknown): OpenPlayQuestion {
  const parsed = playQuestionSchema.parse(raw);
  if (parsed.locked) throw new Error("parsed as a locked question");
  return parsed;
}

describe("playQuestionSchema", () => {
  const base = {
    locked: false,
    id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
    kind: "choice",
    points: 10,
    choice_ids: ["a", "b"],
    title: "The greenhouse",
    body_md: "Who was in the greenhouse?",
    choices: { a: "The butler", b: "The gardener" },
    closed: false,
    can_answer: true,
    correct: false,
    points_awarded: 0,
  };

  test("carries the title beside the body", () => {
    expect(parseOpen(base)).toMatchObject({
      title: "The greenhouse",
      bodyMd: "Who was in the greenhouse?",
    });
  });

  test("reads an omitted title as none, and derives nothing from the body", () => {
    const untitled: Record<string, unknown> = { ...base };
    delete untitled.title;
    expect(parseOpen(untitled).title).toBe("");
  });

  test("reads an omitted attempt count as no limit, not as zero", () => {
    expect(parseOpen(base).attemptsRemaining).toBeUndefined();
  });

  test("keeps zero attempts left distinct from no limit", () => {
    expect(parseOpen({ ...base, attempts_remaining: 0 }).attemptsRemaining).toBe(0);
  });

  test("reads a null attempt count the same as an omitted one", () => {
    expect(parseOpen({ ...base, attempts_remaining: null }).attemptsRemaining).toBeUndefined();
  });

  test("carries can_answer and closed as the server sent them, not derived", () => {
    const parsed = parseOpen({ ...base, closed: true, can_answer: false });
    expect(parsed.closed).toBe(true);
    expect(parsed.canAnswer).toBe(false);
  });

  test("a text question with no choices reads as having none", () => {
    const parsed = parseOpen({
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

  test("carries correct and points_awarded as the server sent them", () => {
    const parsed = parseOpen({
      ...base,
      closed: true,
      can_answer: false,
      correct: true,
      points_awarded: 8,
    });
    expect(parsed.correct).toBe(true);
    expect(parsed.pointsAwarded).toBe(8);
  });

  test("a question never answered correctly carries no points", () => {
    const parsed = parseOpen(base);
    expect(parsed.correct).toBe(false);
    expect(parsed.pointsAwarded).toBe(0);
  });

  test("an open question says it is not locked", () => {
    expect(parseOpen(base).locked).toBe(false);
  });

  test("reads an open question that omits the marker as open", () => {
    const unmarked: Record<string, unknown> = { ...base };
    delete unmarked.locked;
    expect(parseOpen(unmarked)).toMatchObject({ locked: false, bodyMd: "Who was in the greenhouse?" });
  });
});

describe("a locked question", () => {
  test("parses as its number and the marker, a shape with no content at all", () => {
    expect(playQuestionSchema.parse({ position: 3, locked: true })).toEqual({ locked: true, position: 3 });
  });

  // The server never sends more; if it ever did, nothing of it reaches the screen.
  test("keeps nothing but its number, whatever else arrives beside it", () => {
    const parsed = playQuestionSchema.parse({
      position: 2,
      locked: true,
      id: "q2",
      title: "The weapon",
      body_md: "Which weapon?",
      points: 20,
    });
    expect(parsed).toEqual({ locked: true, position: 2 });
  });

  test("refuses a locked entry without a usable number", () => {
    expect(() => playQuestionSchema.parse({ locked: true })).toThrow();
    expect(() => playQuestionSchema.parse({ locked: true, position: 0 })).toThrow();
    expect(() => playQuestionSchema.parse({ locked: true, position: 1.5 })).toThrow();
  });
});

describe("playQuestionListSchema", () => {
  test("parses an empty list rather than requiring at least one question", () => {
    expect(playQuestionListSchema.parse({ lang: "en", items: [] })).toEqual({ lang: "en", items: [] });
  });

  test("parses open and locked questions side by side, in the order sent", () => {
    const open = {
      locked: false,
      id: "q1",
      kind: "text",
      points: 10,
      choice_ids: [],
      title: "",
      body_md: "Who?",
      closed: false,
      can_answer: true,
      correct: false,
      points_awarded: 0,
    };
    const items = playQuestionListSchema.parse({ lang: "en", items: [open, { position: 2, locked: true }] }).items;
    expect(items.map((item) => item.locked)).toEqual([false, true]);
    expect(items[1]).toEqual({ locked: true, position: 2 });
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
