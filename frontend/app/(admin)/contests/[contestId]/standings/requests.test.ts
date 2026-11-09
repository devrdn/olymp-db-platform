import { afterEach, describe, expect, test, vi } from "vitest";

import { fetchStaffStandings } from "./requests";

const contestId = "11111111-1111-1111-1111-111111111111";

const calls: { url: string; init: RequestInit }[] = [];

function answer(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
    }),
  );
}

afterEach(() => {
  calls.length = 0;
  vi.unstubAllGlobals();
});

describe("fetchStaffStandings", () => {
  test("asks the API itself for the live table, with the session cookie", async () => {
    answer(200, {
      shown: { state: "live" },
      status: "running",
      scoring: "points",
      freeze_min: null,
      names: "login",
      generated_at: "2026-09-20T10:00:00Z",
      truncated: false,
      rows: [],
    });

    const result = await fetchStaffStandings(contestId);

    expect(result.kind).toBe("ok");
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/leaderboard/live`);
    expect(calls[0].init.credentials).toBe("same-origin");
  });

  test("returns the API's code when the API refuses", async () => {
    answer(401, { error: { code: "unauthenticated", message: "signed out" } });

    expect(await fetchStaffStandings(contestId)).toEqual({ kind: "refused", code: "unauthenticated" });
  });

  test("calls a dropped connection unreachable rather than throwing", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));

    expect(await fetchStaffStandings(contestId)).toEqual({ kind: "refused", code: "unreachable" });
  });

  test("sends no request for something that is not a contest id", async () => {
    answer(200, {});

    expect(await fetchStaffStandings("../users")).toEqual({ kind: "refused", code: "not_found" });
    expect(calls).toHaveLength(0);
  });
});
