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
        is_visible: true,
        choice_ids: [],
        texts: { en: { body_md: "Who did it?" } },
        answers: [{ match_kind: "exact_ci", value: "the butler" }],
      },
    });
  });

  test("treats unlimited attempts as absent, never as zero", () => {
    // A question allowing zero attempts is one nobody can answer, which is
    // never what an empty box meant.
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
    // "Not written yet" and "deliberately empty" are different facts, and an
    // empty string satisfies the publish gate's presence check — publishing a
    // question that asks its Romanian readers nothing.
    const parsed = questionFrom(form([...base, ["body.en", "Who?"], ["body.ro", "   "]]));

    expect(parsed).toMatchObject({ body: { texts: { en: { body_md: "Who?" } } } });
    expect((parsed as { body: { texts: Record<string, unknown> } }).body.texts.ro).toBeUndefined();
  });

  test("keeps an option label only where the question has words", () => {
    // A label attached to a language with no body is an option for a question
    // that does not exist in that language.
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
    // The API refuses options on any other kind, so switching back to typed
    // text has to let go of them here.
    expect(questionFrom(form([...base, ["choiceIds", "a, b"]]))).toMatchObject({
      body: { choice_ids: [] },
    });
  });

  test("takes each option identifier once, however it was typed", () => {
    const parsed = questionFrom(
      // Spaces, commas, semicolons and a real newline: how a list actually
      // arrives when somebody pastes it out of a document.
      form([["kind", "choice"], ["points", "5"], ["choiceIds", "a b,a; b\nc"]]),
    );

    expect(parsed).toMatchObject({ body: { choice_ids: ["a", "b", "c"] } });
  });

  test("ignores an answer row left empty", () => {
    // The editor offers a spare row. An untouched one is not an answer.
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
