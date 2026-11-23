import { describe, expect, test } from "vitest";

import { browserCall, browserDownload, browserRequest } from "./browser";

function recording(response: () => Response) {
  const seen: { url: string; init?: RequestInit }[] = [];
  const fetchImpl: typeof fetch = async (url, init) => {
    seen.push({ url: String(url), init });
    return response();
  };
  return { seen, fetchImpl };
}

describe("browserRequest", () => {
  test("goes to the page's own origin with the session cookie", async () => {
    const { seen, fetchImpl } = recording(() => new Response("[]", { status: 200 }));

    await browserRequest("/contests/c1/play/notes", { method: "PUT", body: { body: "" }, fetchImpl });
    await browserDownload("/contests/c1/results/export", { method: "POST", fetchImpl });

    expect(seen.map((call) => call.url)).toEqual(["/api/v1/contests/c1/play/notes", "/api/v1/contests/c1/results/export"]);
    expect(seen.map((call) => call.init?.credentials)).toEqual(["same-origin", "same-origin"]);
  });
});

describe("browserCall", () => {
  test("gives the success state the call built", async () => {
    const { fetchImpl } = recording(() => new Response(JSON.stringify({ n: 1 }), { status: 200 }));

    const state = await browserCall(async () => ({ kind: "ok" as const, n: (await browserRequest("/x", { fetchImpl })) as unknown }));

    expect(state).toEqual({ kind: "ok", n: { n: 1 } });
  });

  test("turns a refusal into the refused state with what the API said, but not its developer message", async () => {
    const { fetchImpl } = recording(
      () =>
        new Response(JSON.stringify({ error: { code: "reset_too_often", message: "reset limit reached for registration 42", request_id: "req-7" } }), {
          status: 429,
        }),
    );

    expect(await browserCall(() => browserRequest("/x", { fetchImpl }))).toEqual({
      kind: "refused",
      code: "reset_too_often",
      requestId: "req-7",
    });
  });
});
