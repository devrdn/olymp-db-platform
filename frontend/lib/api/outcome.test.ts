import { notFound, redirect } from "next/navigation";
import { afterEach, describe, expect, test, vi } from "vitest";
import { z } from "zod";

import { ApiError, request } from "./client";
import { bestEffort, failureCodeOf, outcome } from "./outcome";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** The API's error envelope, as the Go service writes it. */
function refusing(status: number, envelope: unknown, headers: Record<string, string> = {}): typeof fetch {
  return async () =>
    new Response(JSON.stringify(envelope), {
      status,
      headers: { "content-type": "application/json", ...headers },
    });
}

function answering(body: unknown): typeof fetch {
  return async () => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}

describe("outcome", () => {
  test("gives the call's value when the API answered", async () => {
    const fetchImpl = answering({ id: "c1" });

    expect(await outcome(() => request("/contests/c1", { fetchImpl }))).toEqual({ ok: true, value: { id: "c1" } });
  });

  test("keeps everything the API said about a refusal, and nothing it did not say", async () => {
    const fetchImpl = refusing(
      400,
      { error: { code: "query_syntax", message: "syntax error at or near FORM", request_id: "req-9" }, subject: "orders", position: 12 },
      { "retry-after": "30" },
    );

    expect(await outcome(() => request("/contests/c1/query", { fetchImpl }))).toEqual({
      ok: false,
      code: "query_syntax",
      requestId: "req-9",
      subject: "orders",
      position: 12,
      retryAfterSeconds: 30,
      detail: "syntax error at or near FORM",
    });

    const bare = await outcome(() =>
      request("/contests/c1", { fetchImpl: refusing(409, { error: { code: "not_editable", message: "" } }) }),
    );
    // No key for what the API left out, so a state carries only what it was told.
    expect(Object.keys(bare).sort()).toEqual(["code", "ok"]);
  });

  test("counts a gateway's page, a dead network and a deadline as unreachable", async () => {
    const gateway: typeof fetch = async () => new Response("<html>502 Bad Gateway</html>", { status: 502 });
    const down: typeof fetch = async () => {
      throw new TypeError("fetch failed");
    };
    const late: typeof fetch = async () => {
      throw new DOMException("signal timed out", "TimeoutError");
    };

    for (const fetchImpl of [gateway, down, late]) {
      expect(await outcome(() => request("/contests", { fetchImpl }))).toEqual({ ok: false, code: "unreachable" });
    }
  });

  test("counts an answer that does not parse as unreachable, never as a value", async () => {
    const fetchImpl = answering({ unexpected: true });

    const result = await outcome(async () => z.object({ id: z.string() }).parse(await request("/contests/c1", { fetchImpl })));

    expect(result).toEqual({ ok: false, code: "unreachable" });
  });

  test("lets a caller's own abort through, since a cancelled call is no failure to show", async () => {
    const controller = new AbortController();
    controller.abort();
    const fetchImpl: typeof fetch = async (_url, init) => {
      init?.signal?.throwIfAborted();
      return new Response(null, { status: 204 });
    };

    await expect(outcome(() => request("/contests", { fetchImpl, signal: controller.signal }))).rejects.toMatchObject({
      name: "AbortError",
    });
  });

  test("rethrows Next's own control flow rather than reporting it as a failure", async () => {
    await expect(
      outcome(async () => {
        redirect("/login");
      }),
    ).rejects.toMatchObject({ digest: expect.stringContaining("NEXT_REDIRECT") });

    await expect(
      outcome(async () => {
        notFound();
      }),
    ).rejects.toMatchObject({ digest: expect.stringContaining("NEXT_HTTP_ERROR_FALLBACK") });
  });
});

describe("outcome on the server", () => {
  /** Everything one failure wrote to the server's log, as one string. */
  function logOf(spy: { mock: { calls: unknown[][] } }): string {
    return spy.mock.calls.map((call) => call.map(String).join(" ")).join("\n");
  }

  test("logs a failure the API did not describe once, by its name, and keeps it out of the outcome", async () => {
    // Next's server has no window; the browser's console is the visitor's.
    vi.stubGlobal("window", undefined);
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    const result = await outcome(async () => {
      throw new RangeError("a bug");
    });

    expect(result).toEqual({ ok: false, code: "unreachable" });
    expect(logged).toHaveBeenCalledTimes(1);
    expect(logOf(logged)).toContain("RangeError");
  });

  test("never logs what a rejected fetch says, which can be the sender's own cookie with a line break", async () => {
    vi.stubGlobal("window", undefined);
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    const rejected = new TypeError(
      'Headers.append: "dbcontest_device=\nFAKE LINE; dbcontest_session=secret-token" is an invalid header value.',
    );

    await outcome(async () => {
      throw rejected;
    });

    const line = logOf(logged);
    expect(line).toContain("TypeError");
    expect(line).not.toContain("secret-token");
    expect(line).not.toContain("FAKE LINE");
    expect(line).not.toMatch(/[\r\n]/);
  });

  test("names the fields of an answer that drifted from the contract, never their values", async () => {
    vi.stubGlobal("window", undefined);
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    await outcome(async () => z.object({ login: z.string() }).parse({ login: 42, password: "hunter2" }));

    const line = logOf(logged);
    expect(line).toContain("ZodError");
    expect(line).toContain("login");
    expect(line).not.toContain("42");
    expect(line).not.toContain("hunter2");
  });

  test("does not log a refusal the API gave in its own terms", async () => {
    vi.stubGlobal("window", undefined);
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    await outcome(async () => {
      throw new ApiError("not_editable", 409, "not editable");
    });

    expect(logged).not.toHaveBeenCalled();
  });

  test("logs every failure of a read it is told to report, once, with the path and the request id", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    await outcome(
      async () => {
        throw new ApiError("internal_error", 500, "pq: password authentication failed", "req-7\nFAKE");
      },
      { report: "/users/mail-status" },
    );
    await outcome(
      async () => {
        throw new ApiError("unreachable", 504, "No answer in time from http://api:8080/api/v1/users/mail-status");
      },
      { report: "/users/mail-status" },
    );

    expect(logged).toHaveBeenCalledTimes(2);
    const line = logOf(logged);
    expect(line).toContain("/users/mail-status");
    expect(line).toContain("internal_error");
    expect(line).toContain("req-7");
    expect(line).toContain("unreachable");
    expect(line).not.toContain("pq:");
    expect(line).not.toContain("api:8080");
    for (const [entry] of logged.mock.calls) expect(String(entry)).not.toMatch(/[\r\n]/);
  });

  test("lets no line or paragraph separator into a log line, since a log viewer breaks lines on them too", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    await outcome(
      async () => {
        throw new ApiError("internal_error", 500, "", "req-7\u2028FAKE\u2029LINE");
      },
      { report: "/users/mail-status" },
    );

    expect(logOf(logged)).not.toMatch(/[\u2028\u2029]/);
  });
});

describe("bestEffort", () => {
  test("gives the value of a call that worked", async () => {
    expect(await bestEffort(async () => "who", "/auth/me")).toBe("who");
  });

  test("is null, with the failure logged under the path read, for a call that failed", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    expect(await bestEffort(async () => Promise.reject(new ApiError("internal_error", 500, "")), "/settings")).toBeNull();
    expect(String(logged.mock.calls[0]?.[0])).toContain("/settings");
  });
});

describe("failureCodeOf", () => {
  test("is the API's code when it answered, unreachable for anything else", () => {
    expect(failureCodeOf(new ApiError("not_editable", 409, ""))).toBe("not_editable");
    expect(failureCodeOf(new TypeError("fetch failed"))).toBe("unreachable");
  });
});
