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

  // A game upload's chunk is a Blob straight off `File.slice`, sent exactly
  // as raw bytes go — no content type, no wrapping JSON — the same as the
  // ArrayBuffer the settings-image upload already sends this way.
  test("sends a Blob body as-is, with no content type declared for it", async () => {
    let seen: RequestInit | undefined;
    const fetchImpl: typeof fetch = async (_url, init) => {
      seen = init;
      return new Response(null, { status: 204 });
    };
    const chunk = new Blob([new Uint8Array([1, 2, 3])]);

    await request("/contests/1/game/uploads/u1/chunk?offset=0", {
      method: "PUT",
      rawBody: chunk,
      fetchImpl,
    });

    expect(seen?.body).toBe(chunk);
    expect(seen?.headers).not.toMatchObject({ "content-type": expect.anything() });
  });

  // The one thing a browser-side upload loop needs that a Server Action
  // never does: a way to cancel a request already in flight.
  test("carries an abort signal through to fetch", async () => {
    let seen: RequestInit | undefined;
    const fetchImpl: typeof fetch = async (_url, init) => {
      seen = init;
      return new Response(null, { status: 204 });
    };
    const controller = new AbortController();

    await request("/x", { fetchImpl, signal: controller.signal });

    expect(seen?.signal).toBe(controller.signal);
  });
});

describe("what an error is about", () => {
  // "A function is not available" without saying which is the unactionable
  // answer this exists to prevent.
  test("carries the subject the server named beside the code", async () => {
    const fetchImpl = respondWith(
      { error: { code: "query_function_not_supported", message: "not allowed" }, subject: "pg_sleep" },
      400,
    );

    await expect(request("/x", { fetchImpl })).rejects.toMatchObject({
      code: "query_function_not_supported",
      subject: "pg_sleep",
    });
  });

  test("leaves the subject undefined when the server named none", async () => {
    const fetchImpl = respondWith({ error: { code: "invalid_request", message: "no" } }, 400);

    await expect(request("/x", { fetchImpl })).rejects.toMatchObject({
      code: "invalid_request",
      subject: undefined,
    });
  });

  // A syntax error is the one refusal that names a place in the text — the
  // console needs the character PostgreSQL's own parser pointed at, not just
  // its words.
  test("carries the position a syntax error named beside the code", async () => {
    const fetchImpl = respondWith(
      { error: { code: "query_parse_error", message: "bad" }, subject: 'syntax error at or near "FRO"', position: 15 },
      400,
    );

    await expect(request("/x", { fetchImpl })).rejects.toMatchObject({
      code: "query_parse_error",
      position: 15,
    });
  });

  test("leaves the position undefined when the server named none", async () => {
    const fetchImpl = respondWith({ error: { code: "query_too_long", message: "no" } }, 400);

    await expect(request("/x", { fetchImpl })).rejects.toMatchObject({
      code: "query_too_long",
      position: undefined,
    });
  });
});
