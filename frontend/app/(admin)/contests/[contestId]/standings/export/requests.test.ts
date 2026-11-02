import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import type { ResultsExportBody } from "@/lib/api/results";

import { downloadResultsCsv, fetchResultsReport } from "./requests";

const contestId = "11111111-1111-1111-1111-111111111111";

const body: ResultsExportBody = {
  format: "csv",
  registrations: ["a1"],
  detail: "summary",
  fields: ["full_name"],
  csv: { excel: false },
};

const wire = {
  contest: {
    id: contestId,
    titles: { en: "Olympiad" },
    default_language: "en",
    status: "finished",
    scoring: "points",
    icpc_penalty_min: 20,
    prize_places: { first: 1, second: 0, third: 0 },
  },
  status: "final",
  hidden_by_freeze: false,
  generated_at: "2026-11-02T12:00:00Z",
  registered: 1,
  detail: "summary",
  fields: ["full_name"],
  bonus_credit: false,
  questions: [],
  rows: [
    {
      registration_id: "a1",
      place: 1,
      prize: 1,
      full_name: "Ana Pop",
      disqualified: false,
      solved: 1,
      penalty: null,
      points: 10,
      last_solved_at: null,
      bonus_credit: null,
    },
  ],
  fingerprint: "0a1b2c3d",
};

const calls: { url: string; init: RequestInit }[] = [];

function answer(response: () => Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return response();
    }),
  );
}

function refusal(status: number, code: string) {
  return () =>
    new Response(JSON.stringify({ error: { code, message: "refused" } }), {
      status,
      headers: { "content-type": "application/json" },
    });
}

beforeEach(() => {
  calls.length = 0;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("downloadResultsCsv", () => {
  test("downloadResultsCsv posts the body to the export endpoint and returns the file", async () => {
    answer(
      () =>
        new Response("place\r\n1\r\n", {
          status: 200,
          headers: {
            "content-type": "text/csv; charset=utf-8",
            "content-disposition": 'attachment; filename="results-server-20261102T120000Z.csv"',
          },
        }),
    );

    const outcome = await downloadResultsCsv(contestId, body);

    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/results/export`);
    expect(calls[0].init).toMatchObject({ method: "POST", credentials: "same-origin" });
    expect(JSON.parse(String(calls[0].init.body))).toEqual(body);
    expect(outcome.kind).toBe("ok");
    if (outcome.kind !== "ok") return;
    expect(outcome.fileName).toBe("results-server-20261102T120000Z.csv");
    expect(await outcome.blob.text()).toBe("place\r\n1\r\n");
  });

  test("names the file itself when the server sends no name", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-02T12:34:56Z"));
    answer(() => new Response("place\r\n", { status: 200, headers: { "content-type": "text/csv" } }));

    const outcome = await downloadResultsCsv(contestId, body);
    vi.useRealTimers();

    expect(outcome).toMatchObject({ kind: "ok", fileName: `results-${contestId}-20261102T123456Z.csv` });
  });

  test("maps an API error to its code", async () => {
    answer(refusal(429, "results_export_too_often"));

    expect(await downloadResultsCsv(contestId, body)).toEqual({ kind: "refused", code: "results_export_too_often" });
  });

  test("refuses an id that is not one without a request", async () => {
    answer(() => new Response(null, { status: 500 }));

    expect(await downloadResultsCsv("../users", body)).toEqual({ kind: "refused", code: "not_found" });
    expect(calls).toHaveLength(0);
  });
});

describe("fetchResultsReport", () => {
  test("fetchResultsReport parses the report and maps an API error to its code", async () => {
    answer(() => new Response(JSON.stringify(wire), { status: 200, headers: { "content-type": "application/json" } }));

    const outcome = await fetchResultsReport(contestId, { ...body, format: "json", csv: undefined });

    expect(calls[0].url).toBe(`/api/v1/contests/${contestId}/results/export`);
    expect(calls[0].init).toMatchObject({ method: "POST", credentials: "same-origin" });
    expect(outcome).toMatchObject({ kind: "ok", report: { status: "final", rows: [{ id: "a1", points: 10 }] } });

    answer(refusal(422, "results_unknown_registration"));
    expect(await fetchResultsReport(contestId, body)).toEqual({ kind: "refused", code: "results_unknown_registration" });
  });

  test("calls a network failure unreachable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );

    expect(await fetchResultsReport(contestId, body)).toEqual({ kind: "refused", code: "unreachable" });
  });
});
