import { describe, expect, test } from "vitest";

import { ApiError, request } from "./client";

/** A fetch stand-in that answers once with the given body and status. */
function respondWith(body: unknown, status: number): typeof fetch {
  return async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { "content-type": "application/json" },
    });
}

describe("request", () => {
  test("returns nothing for a 204, without trying to read a body", async () => {
    const fetchImpl: typeof fetch = async () => new Response(null, { status: 204 });

    await expect(
      request("/contests/1/languages", { method: "PUT", fetchImpl }),
    ).resolves.toBeUndefined();
  });

  test("raises the machine code the server sent, not its human message", async () => {
    const fetchImpl = respondWith(
      {
        error: {
          code: "enrollment_closed",
          message: "Enrollment is closed",
          request_id: "req-7c1",
        },
      },
      409,
    );

    const failure = await request("/contests/1/enroll", {
      method: "POST",
      fetchImpl,
    }).catch((e: unknown) => e);

    expect(failure).toBeInstanceOf(ApiError);
    expect(failure).toMatchObject({
      code: "enrollment_closed",
      status: 409,
      requestId: "req-7c1",
    });
  });

  test("still raises a typed failure when the gateway answers with HTML", async () => {
    const fetchImpl: typeof fetch = async () =>
      new Response("<html><body>502 Bad Gateway</body></html>", {
        status: 502,
        headers: { "content-type": "text/html" },
      });

    const failure = await request("/contests", { fetchImpl }).catch((e: unknown) => e);

    expect(failure).toBeInstanceOf(ApiError);
    expect(failure).toMatchObject({ code: "unreachable", status: 502 });
  });

  test("sends a JSON body to the versioned API path", async () => {
    let seen: { url: string; init?: RequestInit } | undefined;
    const fetchImpl: typeof fetch = async (url, init) => {
      seen = { url: String(url), init };
      return new Response(null, { status: 204 });
    };

    await request("/contests/1/status", {
      method: "POST",
      body: { status: "published" },
      fetchImpl,
    });

    expect(seen?.url).toBe("/api/v1/contests/1/status");
    expect(seen?.init?.headers).toMatchObject({ "content-type": "application/json" });
    expect(seen?.init?.body).toBe('{"status":"published"}');
  });
});
