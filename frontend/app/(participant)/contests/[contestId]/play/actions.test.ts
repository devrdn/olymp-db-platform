import { beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/i18n/server", () => ({ activeLocale: async () => "en" }));

import {
  fetchQueryLogAction,
  fetchStandingsAction,
  refreshQuestionsAction,
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
