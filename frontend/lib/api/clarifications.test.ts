import { describe, expect, test } from "vitest";

import {
  announcementListSchema,
  clarificationFeedSchema,
  clarificationQueueSchema,
  latestStamp,
  staffClarificationSchema,
} from "./clarifications";

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

describe("clarificationFeedSchema", () => {
  test("reads both kinds of item into the interface's own names", () => {
    const parsed = clarificationFeedSchema.parse({
      items: [
        { kind: "announcement", id: "a1", posted_at: "2026-11-02T09:06:00.000000Z", body: "Ten minutes." },
        clarification,
      ],
      next: "cursor",
      open: 2,
    });

    expect(parsed).toEqual({
      items: [
        { kind: "announcement", id: "a1", postedAt: "2026-11-02T09:06:00.000000Z", body: "Ten minutes." },
        {
          kind: "clarification",
          id: "c1",
          postedAt: "2026-11-02T09:05:00.000000Z",
          mine: false,
          questionId: "q1",
          body: "Is it inclusive?",
          askedAt: "2026-11-02T09:00:00.000000Z",
          answer: {
            body: "Yes.",
            declined: false,
            visibility: "public",
            answeredAt: "2026-11-02T09:05:00.000000Z",
            editedAt: null,
          },
        },
      ],
      next: "cursor",
      open: 2,
    });
  });

  test("keeps a question waiting as one with no answer", () => {
    const parsed = clarificationFeedSchema.parse({
      items: [{ ...clarification, mine: true, question_id: null, answer: null }],
      next: null,
      open: 1,
    });
    const item = parsed.items[0];
    expect(item.kind === "clarification" && item.answer).toBeNull();
    expect(item.kind === "clarification" && item.questionId).toBeNull();
  });

  test("drops an unknown kind rather than guessing what it is", () => {
    expect(() =>
      clarificationFeedSchema.parse({ items: [{ kind: "poll", id: "x", posted_at: "t" }], next: null, open: 0 }),
    ).toThrow();
  });
});

describe("staffClarificationSchema", () => {
  test("names the asker and who answered", () => {
    const parsed = staffClarificationSchema.parse({
      id: "c1",
      question_id: null,
      body: "Why?",
      created_at: "2026-11-02T09:00:00.000000Z",
      asker: { login: "ana", full_name: "Ana Ionescu" },
      answer: { ...answer, answered_by: "olga" },
    });
    expect(parsed.asker).toEqual({ login: "ana", fullName: "Ana Ionescu" });
    expect(parsed.answer?.answeredBy).toBe("olga");
  });

  test("reads the queue's counts", () => {
    const parsed = clarificationQueueSchema.parse({ items: [], counts: { open: 3, answered: 4 } });
    expect(parsed.counts).toEqual({ open: 3, answered: 4 });
  });

  test("reads the announcements with their total", () => {
    const parsed = announcementListSchema.parse({
      items: [{ id: "a1", body: "Hello", created_at: "2026-11-02T09:00:00.000000Z" }],
      total: 1,
    });
    expect(parsed.items[0]).toEqual({ id: "a1", body: "Hello", createdAt: "2026-11-02T09:00:00.000000Z" });
  });
});

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
