import { describe, expect, test } from "vitest";

import { clarificationFeedSchema } from "./clarifications";
import { latestStamp } from "./clarifications-terms";

const answer = {
  body: "Yes.",
  declined: false,
  visibility: "public",
  answered_at: "2026-11-02T09:05:00.000000Z",
  edited_at: null,
};

const clarification = {
  kind: "clarification",
  id: "c1",
  posted_at: "2026-11-02T09:05:00.000000Z",
  mine: false,
  question_id: "q1",
  body: "Is it inclusive?",
  asked_at: "2026-11-02T09:00:00.000000Z",
  answer,
};

describe("latestStamp", () => {
  test("is the newest time anything changed that was not the reader's own question", () => {
    const feed = clarificationFeedSchema.parse({
      items: [
        // Asked by the reader and not answered yet: nothing new to them.
        { ...clarification, id: "mine", mine: true, answer: null, posted_at: "2026-11-02T10:00:00.000000Z" },
        { ...clarification, answer: { ...answer, edited_at: "2026-11-02T09:30:00.000000Z" } },
        { kind: "announcement", id: "a1", posted_at: "2026-11-02T09:10:00.000000Z", body: "x" },
      ],
      next: null,
      open: 1,
    });
    expect(latestStamp(feed.items)).toBe("2026-11-02T09:30:00.000000Z");
  });

  test("is empty when nothing came from the organisers", () => {
    expect(latestStamp([])).toBe("");
  });
});
