import { describe, expect, test } from "vitest";

import { answerable, questionSchema, storySchema, untranslated } from "./content";

const question = {
  id: "5f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
  ord: 1,
  kind: "choice",
  points: 10,
  max_attempts: 3,
  penalty_pct: 0,
  is_visible: true,
  choice_ids: ["a", "b", "c"],
  texts: {
    en: { body_md: "Who was in the greenhouse?", choices: { a: "The butler", b: "The gardener" } },
    ro: { body_md: "Cine era în seră?", choices: { a: "Majordomul", b: "Grădinarul" } },
  },
  answers: [{ id: "9f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6", match_kind: "exact", value: "a" }],
};

describe("questionSchema", () => {
  test("parses a question with its texts and reference answers", () => {
    const parsed = questionSchema.parse(question);

    expect(parsed).toMatchObject({
      kind: "choice",
      points: 10,
      maxAttempts: 3,
      penaltyPct: 0,
      isVisible: true,
    });
    expect(parsed.choiceIds).toEqual(["a", "b", "c"]);
    expect(parsed.texts.en.bodyMd).toBe("Who was in the greenhouse?");
    expect(parsed.answers[0]).toMatchObject({ matchKind: "exact", value: "a" });
  });

  /** Go writes a nil `*int` as null, and a number input would show "null". */
  test("reads a null attempt limit as no limit", () => {
    expect(questionSchema.parse({ ...question, max_attempts: null }).maxAttempts).toBeUndefined();
  });

  /**
   * The listing endpoint omits reference answers; only the single-question
   * endpoint carries them. An absent list is not an empty question.
   */
  test("reads an omitted answer list as an empty one, not as undefined", () => {
    const listed = questionSchema.parse({ ...question, answers: undefined });

    expect(listed.answers).toEqual([]);
  });

  test("reads a text with no choices as having none", () => {
    const parsed = questionSchema.parse({
      ...question,
      kind: "text",
      choice_ids: [],
      texts: { en: { body_md: "Name the hour." } },
    });

    expect(parsed.texts.en.choices).toEqual({});
  });
});

describe("untranslated", () => {
  test("names the declared languages the question has no text for", () => {
    expect(untranslated(questionSchema.parse(question), ["en", "ro", "ru"])).toEqual(["ru"]);
  });

  /** An empty body is not a translation; the publish gate does not accept one. */
  test("counts a present but empty body as missing", () => {
    const blank = questionSchema.parse({
      ...question,
      texts: { ...question.texts, ru: { body_md: "" } },
    });

    expect(untranslated(blank, ["en", "ro", "ru"])).toEqual(["ru"]);
  });

  test("is empty when every declared language has text", () => {
    expect(untranslated(questionSchema.parse(question), ["en", "ro"])).toEqual([]);
  });
});

describe("answerable", () => {
  test("is false for a question nothing could mark", () => {
    // The author would otherwise find out at the publish gate, which is later
    // and further from the question they were writing.
    expect(answerable(questionSchema.parse({ ...question, answers: [] }))).toBe(false);
    expect(answerable(questionSchema.parse(question))).toBe(true);
  });
});

describe("storySchema", () => {
  test("parses the story as one body per language", () => {
    const parsed = storySchema.parse({
      id: "7f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
      translations: { en: "The greenhouse was locked from the inside." },
      updated_at: "2026-10-02T08:00:00Z",
    });

    expect(parsed.translations.en).toContain("greenhouse");
    expect(parsed.updatedAt).toBe("2026-10-02T08:00:00Z");
  });
});
