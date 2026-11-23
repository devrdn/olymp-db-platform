import { afterEach, describe, expect, test, vi } from "vitest";

// Both read the current request (`headers()`, `cookies()`), which does not
// exist here; what is tested is what `serverRequest` adds to the transport.
vi.mock("./caller", () => ({ callerHeaders: async () => ({ "x-forwarded-for": "203.0.113.7" }) }));
const { sessionHeader } = vi.hoisted(() => ({
  sessionHeader: vi.fn(async (): Promise<Record<string, string>> => ({ cookie: "dbcontest_session=abc" })),
}));
vi.mock("@/lib/auth/session", () => ({ sessionHeader }));
// Next's cache is a port: the recording fake stands in for the one adapter.
const { revalidatePath } = vi.hoisted(() => ({ revalidatePath: vi.fn() }));
vi.mock("next/cache", () => ({ revalidatePath }));

import { ApiError } from "./client";
import { SERVER_REQUEST_TIMEOUT_MS, serverAction, serverOutcome, serverRequest } from "./server";

afterEach(() => {
  vi.unstubAllEnvs();
  revalidatePath.mockReset();
});

describe("serverRequest", () => {
  test("reads the request before the API's address, so a prerender stops there", async () => {
    vi.stubEnv("NODE_ENV", "production");
    vi.stubEnv("API_ORIGIN", "");
    sessionHeader.mockRejectedValueOnce(new Error("prerender bail"));

    await expect(serverRequest("/contests")).rejects.toThrow("prerender bail");
  });

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

describe("serverOutcome", () => {
  test("revalidates the paths it is given once the call succeeded, a layout as a layout", async () => {
    const result = await serverOutcome(async () => ({ id: "c1" }), {
      revalidate: ["/contests", { layout: "/contests/c1" }],
    });

    expect(result).toEqual({ ok: true, value: { id: "c1" } });
    expect(revalidatePath.mock.calls).toEqual([["/contests"], ["/contests/c1", "layout"]]);
  });

  test("revalidates nothing when the call was refused", async () => {
    const result = await serverOutcome(async () => {
      throw new ApiError("not_editable", 409, "not editable");
    }, { revalidate: ["/contests"] });

    expect(result).toEqual({ ok: false, code: "not_editable", detail: "not editable" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe("serverAction", () => {
  const refusal = () => {
    throw new ApiError("query_syntax", 400, "near FORM in orders", "req-1", "orders", 4, 30);
  };

  test("answers ok, or only the refusal's code: the request id and the API's message stay on the server", async () => {
    expect(await serverAction(async () => undefined)).toEqual({ ok: true });

    const refused = await serverAction(async () => refusal(), {
      revalidate: ["/contests"],
      success: (value) => ({ ok: true as const, value }),
    });

    expect(refused).toEqual({ code: "query_syntax" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("gives an action's own failure only the code and the subject, whatever it returns", async () => {
    const state = await serverAction(async () => refusal(), { failure: (failure) => failure });

    expect(state).toEqual({ code: "query_syntax", subject: "orders" });
  });

  test("shapes a success from the answer, after revalidating what changed", async () => {
    const state = await serverAction(async () => ({ added: 2 }), {
      revalidate: [{ layout: "/contests/c1" }],
      success: (result) => ({ ok: true as const, result }),
    });

    expect(state).toEqual({ ok: true, result: { added: 2 } });
    expect(revalidatePath).toHaveBeenCalledWith("/contests/c1", "layout");
  });

  test("lets the answer decide what changed", async () => {
    await serverAction(async () => ({ added: 0 }), { revalidate: (answer) => (answer.added > 0 ? ["/contests"] : []) });

    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("lets an answer the form cannot read surface as a contract break, not as a refusal", async () => {
    await expect(
      serverAction(async () => ({}), {
        success: () => {
          throw new Error("unreadable answer");
        },
      }),
    ).rejects.toThrow("unreadable answer");
  });
});
