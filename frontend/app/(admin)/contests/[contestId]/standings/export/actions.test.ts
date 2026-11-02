import { beforeEach, describe, expect, test, vi } from "vitest";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import { fetchResultsAction } from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";

const wire = {
  contest: {
    id: contestId,
    titles: { en: "Olympiad" },
    default_language: "en",
    status: "running",
    scoring: "icpc",
    icpc_penalty_min: 20,
    prize_places: { first: 1, second: 0, third: 0 },
  },
  status: "preliminary",
  hidden_by_freeze: false,
  generated_at: "2026-11-02T10:00:00Z",
  registered: 1,
  detail: "summary",
  fields: ["full_name", "login"],
  bonus_credit: false,
  questions: [],
  rows: [
    {
      registration_id: "a1",
      place: 1,
      prize: 1,
      full_name: "Ana Pop",
      login: "apop",
      disqualified: false,
      solved: 1,
      penalty: 20,
      points: null,
      last_solved_at: "2026-11-02T09:20:00Z",
      bonus_credit: null,
    },
  ],
  fingerprint: "0a1b2c3d",
  may_export_email: true,
};

beforeEach(() => {
  serverRequest.mockReset();
});

describe("fetchResultsAction", () => {
  test("reads the preview of the contest's results", async () => {
    serverRequest.mockResolvedValueOnce(wire);

    const result = await fetchResultsAction(contestId);

    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/results`);
    expect(result).toMatchObject({ kind: "ok", report: { mayExportEmail: true, rows: [{ id: "a1", place: 1 }] } });
  });

  test("a contest id that is not an id is refused without a request", async () => {
    for (const id of ["../users", "not-an-id", "", `${contestId}/../../auth/logout`]) {
      expect(await fetchResultsAction(id)).toEqual({ kind: "refused", code: "not_found" });
    }
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("passes the API's refusal on as its code, and anything else as unreachable", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("results_too_many_rows", 409, "too many"));
    expect(await fetchResultsAction(contestId)).toEqual({ kind: "refused", code: "results_too_many_rows" });

    serverRequest.mockRejectedValueOnce(new TypeError("fetch failed"));
    expect(await fetchResultsAction(contestId)).toEqual({ kind: "refused", code: "unreachable" });
  });
});
