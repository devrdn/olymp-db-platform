import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

// The browser's cookie jar, the forwarded headers and the language are the
// request's; the API is a stubbed `fetch`, so the real relay runs.
const { jar, redirect } = vi.hoisted(() => ({
  jar: { get: vi.fn(), set: vi.fn() },
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("next/headers", () => ({ cookies: async () => jar }));
vi.mock("next/navigation", () => ({ redirect }));
vi.mock("@/lib/api/caller", () => ({ callerHeaders: async () => ({ "x-forwarded-for": "203.0.113.7" }) }));
vi.mock("@/lib/i18n/server", () => ({ activeLocale: async () => "ro" }));

import { signUpAction } from "./actions";

const fetchMock = vi.fn<typeof fetch>();

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

const filled = {
  full_name: " Ana Popescu ",
  login: "Ana_P",
  email: " Ana@Example.com ",
  password: "a long enough secret",
  confirm: "a long enough secret",
};

function answer(status: number, body: unknown, setCookie?: string) {
  const headers = new Headers({ "content-type": "application/json" });
  if (setCookie) headers.append("set-cookie", setCookie);
  return new Response(JSON.stringify(body), { status, headers });
}

beforeEach(() => {
  vi.stubEnv("COOKIE_SECURE", "true");
  vi.stubEnv("API_ORIGIN", "http://api.test");
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  jar.get.mockReset();
  jar.set.mockReset();
  redirect.mockClear();
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("signUpAction", () => {
  test("sends the account in the reader's language, from the reader's address", async () => {
    fetchMock.mockResolvedValueOnce(answer(202, { status: "code_sent", email: "ana@example.com" }));

    await expect(signUpAction({}, form(filled))).rejects.toThrow(/NEXT_REDIRECT/);

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("http://api.test/api/v1/public/signup?lang=ro");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      full_name: "Ana Popescu",
      login: "ana_p",
      email: "Ana@Example.com",
      password: "a long enough secret",
    });
    expect(new Headers(init?.headers).get("x-forwarded-for")).toBe("203.0.113.7");
  });

  test("hands the sign-up cookie to the browser with the session cookie's protections", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(
        202,
        { status: "code_sent", email: "ana@example.com" },
        "__Host-dbcontest_signup=tok; Path=/; Max-Age=1800; HttpOnly; Secure; SameSite=Lax",
      ),
    );

    await expect(signUpAction({}, form(filled))).rejects.toThrow(/NEXT_REDIRECT/);

    expect(jar.set).toHaveBeenCalledWith("__Host-dbcontest_signup", "tok", {
      path: "/",
      maxAge: 1800,
      httpOnly: true,
      sameSite: "lax",
      secure: true,
    });
  });

  /** Neither the token nor the address goes into the address bar. */
  test("goes on to the confirmation with nothing in the address", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(202, { status: "code_sent", email: "ana@example.com" }, "__Host-dbcontest_signup=tok; Path=/"),
    );

    await expect(signUpAction({}, form(filled))).rejects.toThrow(/NEXT_REDIRECT/);

    expect(redirect).toHaveBeenCalledWith("/signup/confirm");
  });

  test("keeps the address the code went to in a cookie of the sign-up pages", async () => {
    fetchMock.mockResolvedValueOnce(answer(202, { status: "code_sent", email: "ana@example.com" }));

    await expect(signUpAction({}, form(filled))).rejects.toThrow(/NEXT_REDIRECT/);

    expect(jar.set).toHaveBeenCalledWith("__Secure-dbcontest_signup_email", "ana@example.com", {
      path: "/signup",
      maxAge: 1800,
      httpOnly: true,
      sameSite: "lax",
      secure: true,
    });
  });

  test("keeps no address when the API refused the sign-up", async () => {
    fetchMock.mockResolvedValueOnce(answer(409, { error: { code: "login_taken", message: "taken" } }));

    await signUpAction({}, form(filled));

    expect(jar.set).not.toHaveBeenCalled();
  });

  test("refuses a login that breaks the rule without asking the API, keeping what was typed", async () => {
    const state = await signUpAction({}, form({ ...filled, login: "a.b" }));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(state).toEqual({
      code: "invalid_request",
      broken: "login",
      values: { fullName: "Ana Popescu", login: "a.b", email: "Ana@Example.com" },
    });
  });

  test("refuses two different passwords without asking the API, and never returns them", async () => {
    const state = await signUpAction({}, form({ ...filled, confirm: "another one entirely" }));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(state.code).toBe("password_mismatch");
    expect(JSON.stringify(state)).not.toContain("secret");
  });

  test("returns the API's code with what was typed", async () => {
    fetchMock.mockResolvedValueOnce(answer(409, { error: { code: "login_taken", message: "taken" } }));

    const state = await signUpAction({}, form(filled));

    expect(state).toEqual({
      code: "login_taken",
      values: { fullName: "Ana Popescu", login: "ana_p", email: "Ana@Example.com" },
    });
    expect(redirect).not.toHaveBeenCalled();
  });

  /** The routes do not exist when the installation does not offer sign-up. */
  test("says sign-up is not offered when the API has no such route", async () => {
    fetchMock.mockResolvedValueOnce(answer(404, { error: { code: "not_found", message: "Resource not found" } }));

    expect((await signUpAction({}, form(filled))).code).toBe("signup_not_available");
  });

  test("says the server is unreachable when the API never answered", async () => {
    fetchMock.mockRejectedValueOnce(new TypeError("fetch failed"));

    expect((await signUpAction({}, form(filled))).code).toBe("unreachable");
  });
});
