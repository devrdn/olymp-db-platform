import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import {
  answerClarification,
  announcementListSchema,
  fetchAnnouncements,
  fetchQueue,
  postAnnouncement,
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

describe("the staff's browser requests", () => {
  const contestId = "11111111-1111-1111-1111-111111111111";
  const clarificationId = "22222222-2222-2222-2222-222222222222";
  const calls: { url: string; init: RequestInit }[] = [];

  function answerWith(status: number, body?: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init: RequestInit) => {
        calls.push({ url, init });
        return body === undefined
          ? new Response(null, { status })
          : new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
      }),
    );
  }

  beforeEach(() => {
    calls.length = 0;
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const staffItem = {
    id: clarificationId,
    question_id: null,
    body: "Why?",
    created_at: "2026-11-02T09:00:00.000000Z",
    asker: { login: "ana", full_name: "Ana" },
    answer: null,
  };

  test.each(["../users", "not-an-id", ""])("sends nothing for the contest id %j", async (id) => {
    answerWith(200, {});
    expect(await fetchQueue(id, "open", 0)).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(await answerClarification(id, clarificationId, { body: "x", declined: false, visibility: "private" }, "answer")).toEqual({
      kind: "refused",
      code: "invalid_contest_id",
    });
    expect(calls).toHaveLength(0);
  });

  test("reads one state of the queue with its counts", async () => {
    answerWith(200, { items: [staffItem], counts: { open: 1, answered: 0 } });

    const result = await fetchQueue(contestId, "answered", 50);
    await fetchQueue(contestId, "open", 500);

    expect(result.kind).toBe("ok");
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/clarifications?state=answered&limit=50&offset=0`);
    // The server's page bound, kept on this side too.
    expect(calls[1].url).toBe(`/api/v1/contests/${contestId}/clarifications?state=open&limit=100&offset=0`);
    expect(calls[0].init).toMatchObject({ credentials: "same-origin" });
  });

  test("answers with POST and edits with PUT", async () => {
    answerWith(200, staffItem);
    const answer = { body: "Yes.", declined: false, visibility: "public" as const };

    await answerClarification(contestId, clarificationId, answer, "answer");
    await answerClarification(contestId, clarificationId, answer, "edit");

    expect(calls.map((c) => [c.init.method, c.url])).toEqual([
      ["POST", `/api/v1/contests/${contestId}/clarifications/${clarificationId}/answer`],
      ["PUT", `/api/v1/contests/${contestId}/clarifications/${clarificationId}/answer`],
    ]);
    expect(JSON.parse(String(calls[0].init.body))).toEqual(answer);
  });

  test("passes a refusal on as its code", async () => {
    answerWith(409, { error: { code: "clarification_already_answered", message: "x" } });
    const result = await answerClarification(contestId, clarificationId, { body: "x", declined: false, visibility: "private" }, "answer");
    expect(result).toEqual({ kind: "refused", code: "clarification_already_answered" });
  });

  test("reads and posts announcements", async () => {
    answerWith(200, { items: [], total: 0 });
    expect(await fetchAnnouncements(contestId, 0)).toEqual({ kind: "ok", list: { items: [], total: 0 } });
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/announcements?limit=50&offset=0`);

    answerWith(201, { id: "a1", body: "Hello", created_at: "2026-11-02T09:00:00.000000Z" });
    const posted = await postAnnouncement(contestId, "Hello");
    expect(posted).toEqual({ kind: "ok", announcement: { id: "a1", body: "Hello", createdAt: "2026-11-02T09:00:00.000000Z" } });
    expect(calls[1].init.method).toBe("POST");
    expect(JSON.parse(String(calls[1].init.body))).toEqual({ body: "Hello" });
  });

  test("sends no empty announcement", async () => {
    answerWith(201, {});
    expect(await postAnnouncement(contestId, "   ")).toEqual({ kind: "refused", code: "clarification_empty" });
    expect(calls).toHaveLength(0);
  });
});

