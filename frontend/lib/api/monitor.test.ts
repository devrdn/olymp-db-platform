import { afterEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "./client";
import {
  answersSchema,
  feedPath,
  feedSchema,
  fetchFeed,
  fetchQueries,
  fetchRevision,
  fetchRoster,
  fetchTimeline,
  monitorCsvHref,
  participantCsvHref,
  participantSchema,
  queriesPath,
  queriesSchema,
  rosterSchema,
  timelinePath,
  workspaceSchema,
} from "./monitor";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";
const REG = "9a1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

const flags = {
  multiple_ips: true,
  parallel_sessions: false,
  long_absence: false,
  answer_without_queries: false,
  large_paste: true,
  identical_queries: false,
};

const rosterRow = {
  registration_id: REG,
  login: "ivanov",
  full_name: "Ivan Ivanov",
  status: "active",
  started_at: "2026-09-20T09:00:00.000Z",
  finished_at: null,
  queries: 120,
  query_errors: 7,
  query_rejected: 2,
  addresses: 2,
  correct: 3,
  wrong: 1,
  page_left: 4,
  away_ms: 95_000,
  pastes: 5,
  ip_changes: 1,
  parallel_sessions: 0,
  last_activity: "2026-09-20T10:14:03.120Z",
  flags,
};

describe("the participants table as the API sends it", () => {
  test("reads a row into the interface's names", () => {
    const parsed = rosterSchema.parse({
      generated_at: "2026-09-20T10:14:05.000Z",
      truncated: false,
      rows: [rosterRow],
    });

    expect(parsed.truncated).toBe(false);
    expect(parsed.rows[0]).toMatchObject({
      registrationId: REG,
      fullName: "Ivan Ivanov",
      status: "active",
      queries: 120,
      queryErrors: 7,
      queryRejected: 2,
      pageLeft: 4,
      awayMs: 95_000,
      ipChanges: 1,
      parallelSessions: 0,
      lastActivity: "2026-09-20T10:14:03.120Z",
      flags: {
        multipleIps: true,
        parallelSessions: false,
        longAbsence: false,
        answerWithoutQueries: false,
        largePaste: true,
        identicalQueries: false,
      },
    });
  });
});

describe("the feed as the API sends it", () => {
  const item = (kind: string, data: unknown, cursor = "c1") => ({
    cursor,
    at: "2026-09-20T10:14:03.120Z",
    kind,
    registration_id: REG,
    login: "ivanov",
    full_name: "Ivan Ivanov",
    data,
  });

  test("reads each kind's data into its own shape", () => {
    const parsed = feedSchema.parse({
      items: [
        item("query", { id: 7, sql: "SELECT 1", sql_truncated: false, status: "running", duration_ms: null, row_count: null }, "a"),
        item("answer", { id: REG, question_id: REG, question_ord: 2, attempt_no: 3, value: "42", correct: true, points_awarded: 5 }, "b"),
        item("page_left", { away_ms: 61_000 }, "c"),
        item("paste", { target: "editor", chars: 812, text: "SELECT *" }, "d"),
        item("ip_changed", { from: "10.0.0.1", to: "10.0.0.2" }, "e"),
        item("parallel_session", { other_ip: "10.0.0.9", user_agent: "Firefox" }, "f"),
        item("tab_renamed", { tab_id: REG, from: "Query 1", to: "Suspects" }, "g"),
        item("sign_in", { ip: "10.0.0.1", user_agent: "Chrome" }, "h"),
        item("started", {}, "i"),
        item("paste", { target: "notes", chars: 3, text: "abc", count: 4 }, "j"),
      ],
      more: true,
      newest: "i",
      oldest: "a",
    });

    expect(parsed.more).toBe(true);
    expect(parsed.newest).toBe("i");
    expect(parsed.items.map((i) => i.detail.type)).toEqual([
      "query",
      "answer",
      "page_left",
      "paste",
      "ip_changed",
      "parallel_session",
      "tab",
      "audit",
      "none",
      "paste",
    ]);
    expect(parsed.items[3].detail).toMatchObject({ type: "paste", chars: 812, count: 1 });
    expect(parsed.items[9].detail).toMatchObject({ type: "paste", target: "notes", count: 4 });
    expect(parsed.items[0].detail).toMatchObject({ type: "query", id: 7, status: "running", durationMs: null });
    expect(parsed.items[1].detail).toMatchObject({ type: "answer", questionOrd: 2, correct: true });
    expect(parsed.items[5].detail).toMatchObject({ otherIp: "10.0.0.9", userAgent: "Firefox" });
    expect(parsed.items[6].detail).toMatchObject({ from: "Query 1", to: "Suspects" });
  });

  /**
   * A kind this build does not know yet, or a payload it cannot read, must
   * not take the whole feed down with it: the row still says who and when.
   */
  test("keeps an item whose kind or data it cannot read", () => {
    const parsed = feedSchema.parse({
      items: [item("teleported", { where: "moon" }), item("page_left", { away_ms: "long" }, "c2")],
      more: false,
    });

    expect(parsed.items.map((i) => i.detail.type)).toEqual(["none", "none"]);
    expect(parsed.items[0].kind).toBe("teleported");
    expect(parsed.newest).toBeUndefined();
  });
});

describe("the addresses the screen asks", () => {
  test("names every feed parameter it was given and nothing else", () => {
    expect(feedPath(CONTEST, {})).toBe(`/contests/${CONTEST}/monitor/feed`);
    expect(
      feedPath(CONTEST, {
        after: "abc",
        kinds: ["query", "answer"],
        participant: REG,
        from: "2026-09-20T10:14:03.120Z",
        until: "2026-09-20T10:14:03.121Z",
        limit: 200,
      }),
    ).toBe(
      `/contests/${CONTEST}/monitor/feed?after=abc&kinds=query%2Canswer&participant=${REG}` +
        "&from=2026-09-20T10%3A14%3A03.120Z&until=2026-09-20T10%3A14%3A03.121Z&limit=200",
    );
    expect(feedPath(CONTEST, { before: "xyz", kinds: [] })).toBe(`/contests/${CONTEST}/monitor/feed?before=xyz`);
  });

  test("offers the contest's CSV as an API path on this origin", () => {
    expect(monitorCsvHref(CONTEST)).toBe(`/api/v1/contests/${CONTEST}/monitor/export.csv`);
  });
});

describe("reading from the browser", () => {
  afterEach(() => vi.unstubAllGlobals());

  test("asks the roster with the session cookie and parses it", async () => {
    const fetchMock = vi.fn(async () =>
      new Response(JSON.stringify({ generated_at: "x", truncated: false, rows: [rosterRow] }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const roster = await fetchRoster(CONTEST);

    expect(roster.rows).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/contests/${CONTEST}/monitor/participants`,
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  test("hands a refusal on with the wait it named", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        new Response(JSON.stringify({ error: { code: "monitor_too_often", message: "slow down" } }), {
          status: 429,
          headers: { "retry-after": "60" },
        }),
      ),
    );

    const failure = await fetchFeed(CONTEST, { after: "abc" }).catch((error: unknown) => error);

    expect(failure).toBeInstanceOf(ApiError);
    expect(failure).toMatchObject({ code: "monitor_too_often", status: 429, retryAfterSeconds: 60 });
  });
});

const wireQuery = (id: number, overrides: Record<string, unknown> = {}) => ({
  cursor: `q${id}`,
  executed_at: "2026-09-20T10:14:03.120Z",
  id,
  sql: `SELECT ${id}`,
  sql_truncated: false,
  status: "ok",
  duration_ms: 12,
  row_count: 3,
  ip: "10.0.0.1",
  ...overrides,
});

describe("one participant as the API sends it", () => {
  test("reads who the participant is", () => {
    expect(
      participantSchema.parse({
        registration_id: REG,
        login: "ivanov",
        full_name: "Ivan Ivanov",
        status: "finished",
        started_at: "2026-09-20T09:00:00.000Z",
        finished_at: null,
      }),
    ).toEqual({
      registrationId: REG,
      login: "ivanov",
      fullName: "Ivan Ivanov",
      status: "finished",
      startedAt: "2026-09-20T09:00:00.000Z",
      finishedAt: null,
    });
  });

  test("reads a page of queries, a failed one with its error and none of the optional fields", () => {
    const parsed = queriesSchema.parse({
      items: [
        wireQuery(9),
        { ...wireQuery(8, { status: "error", error: "division by zero", duration_ms: null, row_count: null }), ip: undefined },
      ],
      more: true,
    });

    expect(parsed.more).toBe(true);
    expect(parsed.items[0]).toEqual({
      cursor: "q9",
      executedAt: "2026-09-20T10:14:03.120Z",
      id: 9,
      sql: "SELECT 9",
      sqlTruncated: false,
      status: "ok",
      error: undefined,
      durationMs: 12,
      rowCount: 3,
      ip: "10.0.0.1",
    });
    expect(parsed.items[1]).toMatchObject({ status: "error", error: "division by zero", durationMs: null, ip: undefined });
  });

  test("reads the answers with the queries that led to each attempt", () => {
    const parsed = answersSchema.parse({
      truncated: false,
      questions: [
        {
          question_id: REG,
          question_ord: 2,
          attempts: [
            {
              id: REG,
              question_id: REG,
              question_ord: 2,
              attempt_no: 1,
              value: "42",
              correct: false,
              points_awarded: 0,
              submitted_at: "2026-09-20T10:20:00.000Z",
              queries: [wireQuery(1)],
              more_queries: 4,
            },
          ],
        },
      ],
    });

    expect(parsed.questions[0].questionOrd).toBe(2);
    expect(parsed.questions[0].attempts[0]).toMatchObject({
      attemptNo: 1,
      value: "42",
      correct: false,
      points: 0,
      submittedAt: "2026-09-20T10:20:00.000Z",
      moreQueries: 4,
    });
    expect(parsed.questions[0].attempts[0].queries[0]).toMatchObject({ id: 1, sql: "SELECT 1" });
  });

  test("reads the workspace and its revisions", () => {
    const parsed = workspaceSchema.parse({
      notes: { body: "suspects: 3", updated_at: null },
      tabs: [{ id: REG, title: "Query 1", position: 0, body: "SELECT 1", updated_at: "2026-09-20T10:00:00.000Z" }],
      revisions: [
        {
          id: 5,
          document: "notes",
          title: "",
          started_at: "2026-09-20T10:00:00.000Z",
          updated_at: "2026-09-20T10:00:20.000Z",
          size: 11,
        },
      ],
      truncated: false,
    });

    expect(parsed.notes).toEqual({ body: "suspects: 3", updatedAt: null });
    expect(parsed.tabs[0]).toMatchObject({ id: REG, title: "Query 1", body: "SELECT 1" });
    expect(parsed.revisions[0]).toEqual({
      id: 5,
      document: "notes",
      title: "",
      startedAt: "2026-09-20T10:00:00.000Z",
      updatedAt: "2026-09-20T10:00:20.000Z",
      size: 11,
    });
  });
});

describe("the addresses of one participant", () => {
  test("the timeline takes the feed's parameters but never a participant of its own", () => {
    expect(timelinePath(CONTEST, REG, {})).toBe(`/contests/${CONTEST}/monitor/participants/${REG}/timeline`);
    expect(timelinePath(CONTEST, REG, { after: "abc", kinds: ["sign_in"], participant: "other", limit: 200 })).toBe(
      `/contests/${CONTEST}/monitor/participants/${REG}/timeline?after=abc&kinds=sign_in&limit=200`,
    );
  });

  test("the queries name only the filters that were given", () => {
    expect(queriesPath(CONTEST, REG, {})).toBe(`/contests/${CONTEST}/monitor/participants/${REG}/queries`);
    expect(queriesPath(CONTEST, REG, { status: "error", q: "50%", cursor: "q9" })).toBe(
      `/contests/${CONTEST}/monitor/participants/${REG}/queries?status=error&q=50%25&cursor=q9`,
    );
  });

  test("offers the participant's CSV as an API path on this origin", () => {
    expect(participantCsvHref(CONTEST, REG)).toBe(`/api/v1/contests/${CONTEST}/monitor/participants/${REG}/export.csv`);
  });
});

describe("reading one participant from the browser", () => {
  afterEach(() => vi.unstubAllGlobals());

  function answering(body: unknown) {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify(body), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  test("asks the timeline", async () => {
    const fetchMock = answering({ items: [], more: false });
    await fetchTimeline(CONTEST, REG, { kinds: ["query"] });
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/contests/${CONTEST}/monitor/participants/${REG}/timeline?kinds=query`,
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  test("asks the queries", async () => {
    const fetchMock = answering({ items: [wireQuery(1)], more: false });
    const page = await fetchQueries(CONTEST, REG, { status: "ok" });
    expect(page.items[0].id).toBe(1);
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/contests/${CONTEST}/monitor/participants/${REG}/queries?status=ok`,
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  test("asks one revision whole", async () => {
    const fetchMock = answering({
      id: 5,
      document: "notes",
      title: "",
      started_at: "2026-09-20T10:00:00.000Z",
      updated_at: "2026-09-20T10:00:20.000Z",
      size: 3,
      body: "abc",
    });
    const revision = await fetchRevision(CONTEST, REG, 5);
    expect(revision).toMatchObject({ id: 5, body: "abc" });
    expect(fetchMock).toHaveBeenCalledWith(
      `/api/v1/contests/${CONTEST}/monitor/participants/${REG}/workspace/revisions/5`,
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });
});
