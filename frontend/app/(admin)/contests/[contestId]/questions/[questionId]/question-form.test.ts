import { describe, expect, test } from "vitest";

import { questionFrom } from "./question-form";

const form = (entries: [string, string][]) => {
  const data = new FormData();
  for (const [key, value] of entries) data.append(key, value);
  return data;
};

const base: [string, string][] = [
  ["kind", "text"],
  ["points", "5"],
];

describe("questionFrom", () => {
  test("reads the question, its wording and its answers as one thing", () => {
    const parsed = questionFrom(
      form([
        ...base,
        ["isVisible", "on"],
        ["body.en", "Who did it?"],
        ["answerValue", "the butler"],
        ["answerKind", "exact_ci"],
      ]),
    );

    expect(parsed).toEqual({
      ok: true,
      body: {
        kind: "text",
        points: 5,
        max_attempts: null,
        penalty_pct: null,
        is_visible: true,
        choice_ids: [],
        texts: { en: { body_md: "Who did it?" } },
        answers: [{ match_kind: "exact_ci", value: "the butler" }],
      },
    });
  });

  // A blank penalty must mean "leave it alone": zero is a real setting, and
  // sending it would wipe a penalty set through the API.
  test("leaves the penalty alone when its box is untouched, never zeroing it", () => {
    expect(questionFrom(form(base))).toMatchObject({ body: { penalty_pct: null } });
    expect(questionFrom(form([...base, ["penaltyPct", ""]]))).toMatchObject({
      body: { penalty_pct: null },
    });
  });

  test("reads a configured penalty, zero included, as the real value it is", () => {
    expect(questionFrom(form([...base, ["penaltyPct", "0"]]))).toMatchObject({
      body: { penalty_pct: 0 },
    });
    expect(questionFrom(form([...base, ["penaltyPct", "25"]]))).toMatchObject({
      body: { penalty_pct: 25 },
    });
  });

  test("refuses a penalty outside 0 to 100", () => {
    expect(questionFrom(form([...base, ["penaltyPct", "-1"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
    expect(questionFrom(form([...base, ["penaltyPct", "101"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
    expect(questionFrom(form([...base, ["penaltyPct", "many"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
  });

  test("treats unlimited attempts as absent, never as zero", () => {
    // Zero attempts would be unanswerable, which an empty box never meant.
    expect(questionFrom(form([...base, ["maxAttempts", ""]]))).toMatchObject({
      body: { max_attempts: null },
    });
    expect(questionFrom(form([...base, ["maxAttempts", "0"]]))).toMatchObject({
      body: { max_attempts: null },
    });
    expect(questionFrom(form([...base, ["maxAttempts", "3"]]))).toMatchObject({
      body: { max_attempts: 3 },
    });
  });

  test("drops a language whose body was left blank", () => {
    // An empty string would pass the publish gate's presence check.
    const parsed = questionFrom(form([...base, ["body.en", "Who?"], ["body.ro", "   "]]));

    expect(parsed).toMatchObject({ body: { texts: { en: { body_md: "Who?" } } } });
    expect((parsed as { body: { texts: Record<string, unknown> } }).body.texts.ro).toBeUndefined();
  });

  test("keeps an option label only where the question has words", () => {
    // Labels without a body in that language are dropped.
    const parsed = questionFrom(
      form([
        ["kind", "choice"],
        ["points", "5"],
        ["choiceIds", "a, b"],
        ["body.en", "Who?"],
        ["choice.en.a", "The butler"],
        ["choice.ro.a", "Majordomul"],
        ["answerValue", "a"],
        ["answerKind", "exact"],
      ]),
    );

    expect(parsed).toMatchObject({
      body: {
        choice_ids: ["a", "b"],
        texts: { en: { body_md: "Who?", choices: { a: "The butler" } } },
      },
    });
  });

  test("drops the options when the question is no longer a choice", () => {
    // The API refuses options on other kinds.
    expect(questionFrom(form([...base, ["choiceIds", "a, b"]]))).toMatchObject({
      body: { choice_ids: [] },
    });
  });

  test("takes each option identifier once, however it was typed", () => {
    const parsed = questionFrom(
      // Separators as they arrive from a pasted document.
      form([["kind", "choice"], ["points", "5"], ["choiceIds", "a b,a; b\nc"]]),
    );

    expect(parsed).toMatchObject({ body: { choice_ids: ["a", "b", "c"] } });
  });

  test("ignores an answer row left empty", () => {
    // An untouched spare row is not an answer.
    const parsed = questionFrom(
      form([
        ...base,
        ["answerValue", "the butler"],
        ["answerKind", "exact_ci"],
        ["answerValue", "  "],
        ["answerKind", "exact_ci"],
      ]),
    );

    expect(parsed).toMatchObject({ body: { answers: [{ value: "the butler" }] } });
  });

  test("refuses a kind or a match it has no wording for", () => {
    expect(questionFrom(form([["kind", "riddle"], ["points", "5"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
    expect(
      questionFrom(form([...base, ["answerValue", "x"], ["answerKind", "vibes"]])),
    ).toEqual({ ok: false, code: "invalid_request" });
  });

  test("refuses points that are not a number, or are negative", () => {
    expect(questionFrom(form([["kind", "text"], ["points", "many"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
    expect(questionFrom(form([["kind", "text"], ["points", "-1"]]))).toEqual({
      ok: false,
      code: "invalid_request",
    });
  });
});
