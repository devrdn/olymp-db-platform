import { beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// `vi.mock` factories are hoisted, so their mocks are built with `vi.hoisted`.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
// The real module reads the locale cookie through Next's request APIs.
vi.mock("@/lib/i18n/server", () => ({ activeLocale: async () => "en" }));

import {
  fetchQueryLogAction,
  fetchStandingsAction,
  refreshQuestionsAction,
  refreshSchemaAction,
  resetDatabaseAction,
  runQueryAction,
  submitAnswerAction,
} from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const questionId = "22222222-2222-2222-2222-222222222222";

// Ids that would change which endpoint a path addresses, or are no id at all.
const hostile = ["../users", "not-an-id", "", `${contestId}/../../auth/logout`];

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [name, value] of Object.entries(fields)) data.set(name, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
});

describe("runQueryAction", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const state = await runQueryAction({ kind: "idle" }, form({ contestId: id, sql: "SELECT 1" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("sends the query to the contest's own path", async () => {
    serverRequest.mockResolvedValueOnce({
      columns: [],
      rows: [],
      row_count: 0,
      truncated: false,
      elapsed_ms: 1,
    });

    await runQueryAction({ kind: "idle" }, form({ contestId, sql: "SELECT 1" }));

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/query`, {
      method: "POST",
      body: { sql: "SELECT 1" },
    });
  });

  test("an empty console stays idle without a request", async () => {
    const state = await runQueryAction({ kind: "idle" }, form({ contestId, sql: "  " }));

    expect(state).toEqual({ kind: "idle" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("submitAnswerAction", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const state = await submitAnswerAction({ kind: "idle" }, form({ contestId: id, questionId, value: "42" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test.each(hostile)("sends no request for the question id %j", async (id) => {
    const state = await submitAnswerAction({ kind: "idle" }, form({ contestId, questionId: id, value: "42" }));

    expect(state).toEqual({ kind: "refused", code: "invalid_question_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("an empty answer stays idle without a request", async () => {
    const state = await submitAnswerAction({ kind: "idle" }, form({ contestId, questionId, value: " " }));

    expect(state).toEqual({ kind: "idle" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("refreshQuestionsAction", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await refreshQuestionsAction(id);

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("asks for the questions of the contest itself", async () => {
    serverRequest.mockResolvedValueOnce({ lang: "en", items: [] });

    const result = await refreshQuestionsAction(contestId);

    expect(result).toEqual({ kind: "ok", items: [] });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/play/questions?lang=en`);
  });
});

describe("fetchQueryLogAction", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await fetchQueryLogAction(id, 20, 0);

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("asks for the contest's own log", async () => {
    serverRequest.mockResolvedValueOnce({ items: [], total: 0 });

    const result = await fetchQueryLogAction(contestId, 20, 40);

    expect(result).toEqual({ kind: "ok", items: [], total: 0 });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/play/log?limit=20&offset=40`);
  });
});

describe("fetchStandingsAction", () => {
  test.each(hostile)("sends no request for the contest id %j", async (id) => {
    const result = await fetchStandingsAction(id);

    expect(result).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("resetDatabaseAction", () => {
  test("asks for the caller's database back and reports it done", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await resetDatabaseAction({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "done" });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/play/reset`, { method: "POST" });
  });

  test.each([["../../auth/logout"], [""]])("refuses the contest id %j without calling the server", async (id) => {
    const state = await resetDatabaseAction({ kind: "idle" }, form({ contestId: id }));

    expect(state).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("passes the API's refusal on with its request id", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("reset_too_often", 429, "too often", "req-7"));

    const state = await resetDatabaseAction({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "refused", code: "reset_too_often", requestId: "req-7" });
  });

  test("reports an API it could not reach as unreachable", async () => {
    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));

    const state = await resetDatabaseAction({ kind: "idle" }, form({ contestId }));

    expect(state).toEqual({ kind: "refused", code: "unreachable" });
  });
});

describe("refreshSchemaAction", () => {
  test("re-reads the schema of the caller's database", async () => {
    const schema = {
      truncated: false,
      tables: [{ name: "guests", columns: [{ name: "id", type: "uuid", nullable: false, references: "" }] }],
    };
    serverRequest.mockResolvedValueOnce(schema);

    const result = await refreshSchemaAction(contestId);

    expect(result).toEqual({ kind: "ok", schema });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/play/schema`);
  });

  test("passes a refusal on as its code", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("schema_hidden", 403, "hidden"));

    expect(await refreshSchemaAction(contestId)).toEqual({ kind: "refused", code: "schema_hidden" });
  });

  test("refuses a contest id that is not one without calling the server", async () => {
    expect(await refreshSchemaAction("../log")).toEqual({ kind: "refused", code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
