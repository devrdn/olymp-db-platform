import { afterEach, describe, expect, test, vi } from "vitest";

// Both read the current request (`headers()`, `cookies()`), which does not
// exist here; what is tested is what `serverRequest` adds to the transport.
vi.mock("./caller", () => ({ callerHeaders: async () => ({ "x-forwarded-for": "203.0.113.7" }) }));
vi.mock("@/lib/auth/session", () => ({ sessionHeader: async () => ({ cookie: "dbcontest_session=abc" }) }));

import { SERVER_REQUEST_TIMEOUT_MS, serverRequest } from "./server";

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("serverRequest", () => {
  test("bounds how long the API may take to answer, with a deadline longer than the API's own", async () => {
    vi.stubEnv("API_ORIGIN", "http://api:8080");
    let seen: RequestInit | undefined;
    const fetchImpl: typeof fetch = async (_url, init) => {
      seen = init;
      return new Response(null, { status: 204 });
    };

    await serverRequest("/contests", { fetchImpl });

    expect(seen?.signal).toBeInstanceOf(AbortSignal);
    expect(seen?.signal?.aborted).toBe(false);
    // A query waits in the queue up to QUERY_QUEUE_WAIT (15 s) and then runs
    // for QUERY_DEADLINE (5 s); the API's answer must never be the one cut off.
    expect(SERVER_REQUEST_TIMEOUT_MS).toBeGreaterThan(20_000);
  });

  test("lets a caller's own signal replace the deadline", async () => {
    vi.stubEnv("API_ORIGIN", "http://api:8080");
    const controller = new AbortController();
    let seen: RequestInit | undefined;
    const fetchImpl: typeof fetch = async (_url, init) => {
      seen = init;
      return new Response(null, { status: 204 });
    };

    await serverRequest("/contests", { fetchImpl, signal: controller.signal });

    expect(seen?.signal).toBe(controller.signal);
  });

  test("addresses the API and carries the caller and the session with every call", async () => {
    vi.stubEnv("API_ORIGIN", "http://api:8080");
    let seen: { url: string; init?: RequestInit } | undefined;
    const fetchImpl: typeof fetch = async (url, init) => {
      seen = { url: String(url), init };
      return new Response(null, { status: 204 });
    };

    await serverRequest("/contests", { fetchImpl });

    expect(seen?.url).toBe("http://api:8080/api/v1/contests");
    expect(seen?.init?.headers).toMatchObject({
      "x-forwarded-for": "203.0.113.7",
      cookie: "dbcontest_session=abc",
    });
  });
});
