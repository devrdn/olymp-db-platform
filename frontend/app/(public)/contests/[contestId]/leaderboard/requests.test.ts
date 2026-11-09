import { afterEach, describe, expect, test, vi } from "vitest";

import { fetchPublicStandings } from "./requests";

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

describe("fetchPublicStandings", () => {
  test("asks the API itself for the table in the page's language", async () => {
    answer(200, {
      state: "live",
      scoring: "points",
      title: "",
      generated_at: "2026-09-20T10:00:00Z",
      truncated: false,
      rows: [],
    });

    const result = await fetchPublicStandings(contestId, "ro");

    expect(result.kind).toBe("ok");
    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/leaderboard?lang=ro`);
    expect(calls[0].init.credentials).toBe("same-origin");
  });

  test("returns the API's code when the API refuses", async () => {
    answer(429, { error: { code: "leaderboard_too_often", message: "slow" } });

    expect(await fetchPublicStandings(contestId, "en")).toEqual({ kind: "refused", code: "leaderboard_too_often" });
  });

  test("sends no request for something that is not a contest id", async () => {
    answer(200, {});

    expect(await fetchPublicStandings("../users", "en")).toEqual({ kind: "refused", code: "not_found" });
    expect(calls).toHaveLength(0);
  });
});
