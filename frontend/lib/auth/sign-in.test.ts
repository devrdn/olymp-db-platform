import { describe, expect, test } from "vitest";

import { signIn } from "./sign-in";

function apiAnswering(body: unknown, init: ResponseInit): typeof fetch {
  return async () => new Response(JSON.stringify(body), init);
}

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

    expect(outcome).toEqual({ ok: true, mustChangePassword: false });
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

  test("carries the caller's forwarded address to the API", async () => {
    // Sign-in reaches the API from this server, so without the chain the API
    // throttles and audits the web container instead of the person. The
    // headers are the action's to decide — this layer just must not lose them.
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
});
