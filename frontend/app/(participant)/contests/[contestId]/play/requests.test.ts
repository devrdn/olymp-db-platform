import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import {
  fetchQueryLog,
  fetchStandings,
  refreshQuestions,
  refreshSchema,
  resetDatabase,
  runQuery,
  submitAnswer,
} from "./requests";

const contestId = "11111111-1111-1111-1111-111111111111";
const questionId = "22222222-2222-2222-2222-222222222222";

// Ids that would change which endpoint a path addresses, or are no id at all.
const hostile = ["../users", "not-an-id", "", `${contestId}/../../auth/logout`];

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [name, value] of Object.entries(fields)) data.set(name, value);
  return data;
}

/** The API's error envelope, as `client.ts` reads it. */
function refusal(code: string, extra: { subject?: string; position?: number; request_id?: string } = {}) {
  return {
    error: { code, message: "refused", request_id: extra.request_id },
    subject: extra.subject,
    position: extra.position,
  };
}

const calls: { url: string; init: RequestInit }[] = [];

function answer(status: number, body?: unknown) {
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

/** A proxy that answered for the API, or no answer at all. */
function gatewayPage() {
  vi.stubGlobal("fetch", vi.fn(async () => new Response("<html>502</html>", { status: 502 })));
}
function networkDown() {
  vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("fetch failed"); }));
}

beforeEach(() => {
  // Records nothing by itself, so a test that forgets to answer sees no call.
  answer(500);
});

afterEach(() => {
  calls.length = 0;
  vi.unstubAllGlobals();
});

describe("runQuery", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const state = await runQuery({ kind: "idle" }, form({ contestId: id, sql: "SELECT 1" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test("sends the query from the browser, with the session cookie, to the contest's own path", async () => {
    answer(200, { columns: [], rows: [], row_count: 0, truncated: false, elapsed_ms: 1 });

    const state = await runQuery({ kind: "idle" }, form({ contestId, sql: "SELECT 1" }));

    expect(state.kind).toBe("answer");
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/query`);
    expect(calls[0].init).toMatchObject({ method: "POST", credentials: "same-origin" });
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ sql: "SELECT 1" });
  });

  test("an empty console stays idle without a request", async () => {
    const state = await runQuery({ kind: "idle" }, form({ contestId, sql: "  " }));

    expect(state).toEqual({ kind: "idle" });
    expect(calls).toHaveLength(0);
  });

  test("turns the API's refusal into the console's shape, position and reference included", async () => {
    answer(400, refusal("query_syntax_error", { subject: "near FORM", position: 10, request_id: "req-3" }));

    const state = await runQuery({ kind: "idle" }, form({ contestId, sql: "SELECT 1 FORM t" }));

    expect(state).toEqual({
      kind: "refused",
      code: "query_syntax_error",
      subject: "near FORM",
      requestId: "req-3",
      position: 10,
    });
  });

  test("reports a gateway page and a dead network as unreachable", async () => {
    gatewayPage();
    expect(await runQuery({ kind: "idle" }, form({ contestId, sql: "SELECT 1" }))).toEqual({
      kind: "refused",
      code: "unreachable",
    });

    networkDown();
    expect(await runQuery({ kind: "idle" }, form({ contestId, sql: "SELECT 1" }))).toEqual({
      kind: "refused",
      code: "unreachable",
    });
  });
});

describe("submitAnswer", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const state = await submitAnswer({ kind: "idle" }, form({ contestId: id, questionId, value: "42" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test.each(hostile)("sends no request for the question id %j", async (id) => {
    const state = await submitAnswer({ kind: "idle" }, form({ contestId, questionId: id, value: "42" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_question_id" });
    expect(calls).toHaveLength(0);
  });

  test("an empty answer stays idle without a request", async () => {
    const state = await submitAnswer({ kind: "idle" }, form({ contestId, questionId, value: " " }));

    expect(state).toEqual({ kind: "idle" });
    expect(calls).toHaveLength(0);
  });

  test("sends the trimmed answer to the question's own path", async () => {
    answer(200, { correct: true, points_awarded: 10, closed: true });

    const state = await submitAnswer({ kind: "idle" }, form({ contestId, questionId, value: " 42 " }));

    expect(state.kind).toBe("answer");
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/questions/${questionId}/answer`);
    expect(calls[0].init).toMatchObject({ method: "POST", credentials: "same-origin" });
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ value: "42" });
  });

  test("turns the API's refusal into the form's shape", async () => {
    answer(409, refusal("deadline_passed", { request_id: "req-5" }));

    const state = await submitAnswer({ kind: "idle" }, form({ contestId, questionId, value: "42" }));

    expect(state).toEqual({ kind: "refused", code: "deadline_passed", requestId: "req-5", subject: undefined });
  });

  // The point of leaving server actions: Next sends a page's actions one at
  // a time, and an answer queued behind a twenty-second query could miss the
  // deadline. A browser request has no such queue.
  test("goes out at once while a query is still running", async () => {
    let finishQuery!: () => void;
    const fetchMock = vi.fn(async (url: string) => {
      if (url.endsWith("/query")) {
        await new Promise<void>((resolve) => { finishQuery = resolve; });
        return new Response(JSON.stringify({ columns: [], rows: [], truncated: false, rows_affected: 0 }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return new Response(JSON.stringify({ correct: true, points_awarded: 10, closed: true }), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);

    const query = runQuery({ kind: "idle" }, form({ contestId, sql: "SELECT pg_sleep(20)" }));
    const answered = await submitAnswer({ kind: "idle" }, form({ contestId, questionId, value: "42" }));

    expect(answered.kind).toBe("answer");
    expect(fetchMock).toHaveBeenCalledTimes(2);

    finishQuery();
    expect((await query).kind).toBe("answer");
  });
});

describe("refreshQuestions", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await refreshQuestions(id, "en");

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test("asks for the questions of the contest itself, in the page's language", async () => {
    answer(200, { lang: "ro", items: [] });

    const result = await refreshQuestions(contestId, "ro");

    expect(result).toEqual({ kind: "ok", items: [] });
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/play/questions?lang=ro`);
    expect(calls[0].init).toMatchObject({ credentials: "same-origin" });
  });

  test("passes a refusal on as its code", async () => {
    answer(403, refusal("not_a_participant"));

    expect(await refreshQuestions(contestId, "en")).toEqual({ kind: "refused", code: "not_a_participant" });
  });
});

describe("fetchQueryLog", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await fetchQueryLog(id, 20, 0);

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test("asks for the contest's own log", async () => {
    answer(200, { items: [], total: 0 });

    const result = await fetchQueryLog(contestId, 20, 40);

    expect(result).toEqual({ kind: "ok", items: [], total: 0 });
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/play/log?limit=20&offset=40`);
  });

  test("passes a refusal on as its code", async () => {
    answer(429, refusal("play_read_too_often"));

    expect(await fetchQueryLog(contestId, 20, 0)).toEqual({ kind: "refused", code: "play_read_too_often" });
  });
});

describe("fetchStandings", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await fetchStandings(id, "en");

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test("asks for the table in the page's language", async () => {
    answer(200, {
      state: "live",
      scoring: "points",
      title: "",
      generated_at: "2026-09-20T10:00:00Z",
      truncated: false,
      rows: [],
    });

    const result = await fetchStandings(contestId, "ru");

    expect(result.kind).toBe("ok");
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/play/leaderboard?lang=ru`);
  });

  test("passes a refusal on as its code", async () => {
    answer(429, refusal("leaderboard_too_often"));

    expect(await fetchStandings(contestId, "en")).toEqual({ kind: "refused", code: "leaderboard_too_often" });
  });
});

describe("resetDatabase", () => {
  test("asks for the caller's database back and reports it done", async () => {
    answer(204);

    const state = await resetDatabase({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "done" });
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/play/reset`);
    expect(calls[0].init).toMatchObject({ method: "POST", credentials: "same-origin" });
  });

  test.each([["../../auth/logout"], [""]])("refuses the contest id %j without a request", async (id) => {
    const state = await resetDatabase({ kind: "idle" }, form({ contestId: id }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });

  test("passes the API's refusal on with its request id", async () => {
    answer(429, refusal("reset_too_often", { request_id: "req-7" }));

    const state = await resetDatabase({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "refused", code: "reset_too_often", requestId: "req-7" });
  });

  test("reports an API it could not reach as unreachable", async () => {
    networkDown();

    const state = await resetDatabase({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "refused", code: "unreachable" });
  });
});

describe("refreshSchema", () => {
  test("re-reads the schema of the caller's database", async () => {
    const schema = {
      truncated: false,
      tables: [{ name: "guests", columns: [{ name: "id", type: "uuid", nullable: false, references: "" }] }],
    };
    answer(200, schema);

    const result = await refreshSchema(contestId);

    expect(result).toEqual({ kind: "ok", schema });
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/play/schema`);
  });

  test("passes a refusal on as its code", async () => {
    answer(403, refusal("schema_hidden"));

    expect(await refreshSchema(contestId)).toEqual({ kind: "refused", code: "schema_hidden" });
  });

  test("refuses a contest id that is not one without a request", async () => {
    expect(await refreshSchema("../log")).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(calls).toHaveLength(0);
  });
});
