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
});
