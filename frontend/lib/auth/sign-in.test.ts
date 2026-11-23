import { afterEach, describe, expect, test, vi } from "vitest";

import { DEVICE_COOKIE, signIn } from "./sign-in";

function apiAnswering(body: unknown, init: ResponseInit): typeof fetch {
  return async () => new Response(JSON.stringify(body), init);
}

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("signIn", () => {
  test("hands the session cookie the API issued on to the browser", async () => {
    const handed: { name?: string; value?: string; maxAge?: number } = {};

    const outcome = await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: apiAnswering(
          { user: { id: "u1", login: "strelcova.i" }, must_change_password: false },
          {
            status: 200,
            headers: {
              "content-type": "application/json",
              "set-cookie": "dbcontest_session=abc123; Path=/; Max-Age=43200; HttpOnly",
            },
          },
        ),
        setCookie: (cookie) => Object.assign(handed, cookie),
      },
    );

    expect(outcome).toEqual({ ok: true, value: { mustChangePassword: false } });
    expect(handed).toMatchObject({ name: "dbcontest_session", value: "abc123", maxAge: 43200 });
  });

  test("reports the failure code and sets no cookie when the password is wrong", async () => {
    let cookieWasSet = false;

    const outcome = await signIn(
      { login: "strelcova.i", password: "nope" },
      {
        fetchImpl: apiAnswering(
          { error: { code: "invalid_credentials", message: "Invalid login or password" } },
          { status: 401, headers: { "content-type": "application/json" } },
        ),
        setCookie: () => {
          cookieWasSet = true;
        },
      },
    );

    expect(outcome).toEqual({ ok: false, code: "invalid_credentials" });
    expect(cookieWasSet).toBe(false);
  });

  test("reads a gateway's page as the API being unreachable, never as a verdict on the password", async () => {
    const outcome = await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async () => new Response("<html>502 Bad Gateway</html>", { status: 502 }),
        setCookie: () => {},
      },
    );

    expect(outcome).toEqual({ ok: false, code: "unreachable" });
  });

  test("gives every attempt its own deadline, so a stalled API cannot hold a sign-in open", async () => {
    const signals: (AbortSignal | null | undefined)[] = [];
    const answers = [
      new Response(JSON.stringify({ error: { code: "sign_in_busy" } }), { status: 503 }),
      new Response(JSON.stringify({ must_change_password: false }), { status: 200 }),
    ];

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_url, init) => {
          signals.push(init?.signal);
          return answers.shift()!;
        },
        setCookie: () => {},
        sleep: async () => {},
      },
    );

    expect(signals).toHaveLength(2);
    for (const signal of signals) {
      expect(signal).toBeInstanceOf(AbortSignal);
      expect(signal?.aborted).toBe(false);
    }
    expect(signals[0]).not.toBe(signals[1]);
  });

  test("reads a dead network or a deadline as the API being unreachable, rather than throwing", async () => {
    const failures = [new TypeError("fetch failed"), new DOMException("signal timed out", "TimeoutError")];

    for (const failure of failures) {
      const outcome = await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async () => {
            throw failure;
          },
          setCookie: () => {},
        },
      );

      expect(outcome).toEqual({ ok: false, code: "unreachable" });
    }
  });

  test("issues no session when it cannot read the API's answer to a sign-in that worked", async () => {
    let cookieWasSet = false;

    for (const body of ["<html>proxy</html>", JSON.stringify({ must_change_password: "yes" })]) {
      const outcome = await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async () =>
            new Response(body, {
              status: 200,
              headers: { "set-cookie": "dbcontest_session=abc123; Path=/; Max-Age=43200; HttpOnly" },
            }),
          setCookie: () => {
            cookieWasSet = true;
          },
        },
      );

      expect(outcome).toEqual({ ok: false, code: "unreachable" });
    }
    expect(cookieWasSet).toBe(false);
  });

  test("carries the caller's forwarded address to the API", async () => {
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), {
            status: 401,
            headers: { "content-type": "application/json" },
          });
        },
        setCookie: () => {},
        headers: { "x-forwarded-for": "203.0.113.7", "user-agent": "Mozilla/5.0" },
      },
    );

    expect(sent?.get("x-forwarded-for")).toBe("203.0.113.7");
    expect(sent?.get("user-agent")).toBe("Mozilla/5.0");
    expect(sent?.get("content-type")).toBe("application/json");
  });

  test("sends the browser's device cookie to the API, and nothing else of the jar", async () => {
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), {
            status: 401,
            headers: { "content-type": "application/json" },
          });
        },
        setCookie: () => {},
        deviceToken: "AQID.c2ln",
      },
    );

    expect(DEVICE_COOKIE).toBe("dbcontest_device");
    expect(sent?.get("cookie")).toBe("dbcontest_device=AQID.c2ln");
  });

  test("sends the session the browser still holds beside the device cookie, so the API can end it", async () => {
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), {
            status: 401,
            headers: { "content-type": "application/json" },
          });
        },
        setCookie: () => {},
        deviceToken: "AQID.c2ln",
        sessionToken: "old-session",
      },
    );

    expect(sent?.get("cookie")).toBe("dbcontest_device=AQID.c2ln; dbcontest_session=old-session");
  });

  test("signs in without a cookie whose value could not go into a header, rather than failing", async () => {
    for (const deviceToken of ["AQID\nFAKE LINE", decodeURIComponent("AQID%0AFAKE LINE")]) {
      let sent: Headers | undefined;

      const outcome = await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async (_input, init) => {
            sent = new Headers(init?.headers);
            return new Response(JSON.stringify({ must_change_password: false }), { status: 200 });
          },
          setCookie: () => {},
          deviceToken,
          sessionToken: "old-session",
        },
      );

      expect(outcome).toEqual({ ok: true, value: { mustChangePassword: false } });
      expect(sent?.get("cookie")).toBe("dbcontest_session=old-session");
    }
  });

  test("sends the previous session alone when the browser has no device cookie", async () => {
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), { status: 401 });
        },
        setCookie: () => {},
        sessionToken: "old-session",
      },
    );

    expect(sent?.get("cookie")).toBe("dbcontest_session=old-session");
  });

  test("sends the previous session under the prefixed name where the cookie is Secure", async () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), { status: 401 });
        },
        setCookie: () => {},
        sessionToken: "old-session",
      },
    );

    expect(sent?.get("cookie")).toBe("__Host-dbcontest_session=old-session");
  });

  test("sends no cookie header when the browser has neither cookie", async () => {
    let sent: Headers | undefined;

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async (_input, init) => {
          sent = new Headers(init?.headers);
          return new Response(JSON.stringify({ error: { code: "invalid_credentials" } }), { status: 401 });
        },
        setCookie: () => {},
      },
    );

    expect(sent?.has("cookie")).toBe(false);
  });

  test("hands the device cookie on to the browser alongside the session", async () => {
    const handed: string[] = [];
    const headers = new Headers({ "content-type": "application/json" });
    headers.append("set-cookie", "dbcontest_session=abc123; Path=/; Max-Age=43200; HttpOnly");
    headers.append("set-cookie", "dbcontest_device=AQID.c2ln; Path=/; Max-Age=2592000; HttpOnly");

    await signIn(
      { login: "strelcova.i", password: "correct horse" },
      {
        fetchImpl: async () => new Response(JSON.stringify({ must_change_password: false }), { status: 200, headers }),
        setCookie: (cookie) => handed.push(`${cookie.name}=${cookie.value};${cookie.maxAge}`),
      },
    );

    expect(handed).toEqual(["dbcontest_session=abc123;43200", "dbcontest_device=AQID.c2ln;2592000"]);
  });

  describe("when the API is too busy to check a password", () => {
    const busy = (retryAfter: string | null) =>
      new Response(JSON.stringify({ error: { code: "sign_in_busy" } }), {
        status: 503,
        headers: {
          "content-type": "application/json",
          ...(retryAfter === null ? {} : { "retry-after": retryAfter }),
        },
      });
    const accepted = () =>
      new Response(JSON.stringify({ must_change_password: false }), {
        status: 200,
        headers: { "content-type": "application/json", "set-cookie": "dbcontest_session=abc; Path=/; Max-Age=60" },
      });

    test("waits as long as the API asks and tries once more", async () => {
      const answers = [busy("1"), accepted()];
      const waits: number[] = [];
      let calls = 0;

      const outcome = await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async () => {
            calls++;
            return answers.shift()!;
          },
          setCookie: () => {},
          sleep: async (ms) => {
            waits.push(ms);
          },
        },
      );

      expect(outcome).toEqual({ ok: true, value: { mustChangePassword: false } });
      expect(calls).toBe(2);
      expect(waits).toEqual([1000]);
    });

    test("tries only once more, and reports the second refusal", async () => {
      let calls = 0;

      const outcome = await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async () => {
            calls++;
            return busy("1");
          },
          setCookie: () => {},
          sleep: async () => {},
        },
      );

      expect(outcome).toEqual({ ok: false, code: "sign_in_busy" });
      expect(calls).toBe(2);
    });

    test("never waits longer than a few seconds, whatever the header says", async () => {
      const waits: number[] = [];
      const answers = [busy("3600"), accepted()];

      await signIn(
        { login: "strelcova.i", password: "correct horse" },
        {
          fetchImpl: async () => answers.shift()!,
          setCookie: () => {},
          sleep: async (ms) => {
            waits.push(ms);
          },
        },
      );

      expect(waits).toHaveLength(1);
      expect(waits[0]).toBeLessThanOrEqual(5000);
    });

    test("waits a second when the header is missing or unreadable", async () => {
      for (const header of [null, "soon"]) {
        const waits: number[] = [];
        const answers = [busy(header), accepted()];

        await signIn(
          { login: "strelcova.i", password: "correct horse" },
          {
            fetchImpl: async () => answers.shift()!,
            setCookie: () => {},
            sleep: async (ms) => {
              waits.push(ms);
            },
          },
        );

        expect(waits).toEqual([1000]);
      }
    });

    test("does not retry any other refusal", async () => {
      let calls = 0;

      await signIn(
        { login: "strelcova.i", password: "nope" },
        {
          fetchImpl: async () => {
            calls++;
            return new Response(JSON.stringify({ error: { code: "too_many_attempts" } }), {
              status: 429,
              headers: { "retry-after": "1" },
            });
          },
          setCookie: () => {},
          sleep: async () => {},
        },
      );

      expect(calls).toBe(1);
    });
  });
});
