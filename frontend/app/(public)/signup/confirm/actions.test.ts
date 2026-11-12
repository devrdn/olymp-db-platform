import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { jar, redirect, fetchIdentity } = vi.hoisted(() => ({
  jar: { get: vi.fn(), set: vi.fn() },
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
  fetchIdentity: vi.fn(),
}));
vi.mock("next/headers", () => ({ cookies: async () => jar }));
vi.mock("next/navigation", () => ({ redirect }));
vi.mock("@/lib/api/caller", () => ({ callerHeaders: async () => ({ "x-forwarded-for": "203.0.113.7" }) }));
vi.mock("@/lib/i18n/server", () => ({ activeLocale: async () => "ru" }));
vi.mock("@/lib/auth/session", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/session")>()),
  fetchIdentity,
}));

import { confirmSignupAction, resendCodeAction } from "./actions";

const fetchMock = vi.fn<typeof fetch>();

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

function answer(status: number, body: unknown, setCookies: string[] = [], extra: Record<string, string> = {}) {
  const headers = new Headers({ "content-type": "application/json", ...extra });
  for (const cookie of setCookies) headers.append("set-cookie", cookie);
  return new Response(JSON.stringify(body), { status, headers });
}

/** The browser holds a sign-up cookie and a session from before. */
function browserHolds(cookies: Record<string, string>) {
  jar.get.mockImplementation((name: string) => (name in cookies ? { name, value: cookies[name] } : undefined));
}

const PROTECTIONS = { path: "/", httpOnly: true, sameSite: "lax", secure: false };

beforeEach(() => {
  vi.stubEnv("COOKIE_SECURE", "false");
  vi.stubEnv("API_ORIGIN", "http://api.test");
  vi.stubGlobal("fetch", fetchMock);
  fetchMock.mockReset();
  jar.get.mockReset();
  jar.set.mockReset();
  redirect.mockClear();
  fetchIdentity.mockReset();
  fetchIdentity.mockResolvedValue({ permissions: [] });
  browserHolds({ dbcontest_signup: "tok", dbcontest_session: "old", dbcontest_locale: "ru" });
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("confirmSignupAction", () => {
  test("sends the address and the code with the browser's sign-up and session cookies, and nothing else of the jar", async () => {
    fetchMock.mockResolvedValueOnce(answer(200, { must_change_password: false }));

    await expect(confirmSignupAction({}, form({ email: " ana@example.com ", code: "123 456" }))).rejects.toThrow(
      /NEXT_REDIRECT/,
    );

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("http://api.test/api/v1/public/signup/confirm");
    expect(JSON.parse(String(init?.body))).toEqual({ email: "ana@example.com", code: "123456" });
    expect(new Headers(init?.headers).get("cookie")).toBe("dbcontest_signup=tok; dbcontest_session=old");
    expect(new Headers(init?.headers).get("x-forwarded-for")).toBe("203.0.113.7");
  });

  test("hands the session and device cookies on, and clears the sign-up cookie", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(200, { must_change_password: false }, [
        "dbcontest_signup=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax",
        "dbcontest_session=sess; Path=/; Max-Age=43200; HttpOnly; SameSite=Lax",
        "dbcontest_device=dev; Path=/; Max-Age=31536000; HttpOnly; SameSite=Lax",
      ]),
    );

    await expect(confirmSignupAction({}, form({ email: "ana@example.com", code: "123456" }))).rejects.toThrow(
      /NEXT_REDIRECT/,
    );

    expect(jar.set).toHaveBeenCalledWith("dbcontest_signup", "", { ...PROTECTIONS, maxAge: 0 });
    expect(jar.set).toHaveBeenCalledWith("dbcontest_session", "sess", { ...PROTECTIONS, maxAge: 43200 });
    expect(jar.set).toHaveBeenCalledWith("dbcontest_device", "dev", { ...PROTECTIONS, maxAge: 31536000 });
  });

  test("lands where sign-in would, by the new account's permissions", async () => {
    fetchMock.mockResolvedValueOnce(answer(200, { must_change_password: false }));
    fetchIdentity.mockResolvedValueOnce({ permissions: [] });

    await expect(confirmSignupAction({}, form({ email: "ana@example.com", code: "123456" }))).rejects.toThrow(
      /NEXT_REDIRECT/,
    );

    expect(redirect).toHaveBeenCalledWith("/my");
  });

  test("refuses a code that is not six digits without spending a try", async () => {
    const state = await confirmSignupAction({}, form({ email: "ana@example.com", code: "12345" }));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(state).toEqual({ code: "signup_wrong_code", values: { email: "ana@example.com" } });
  });

  test("returns the API's code, keeping the address", async () => {
    fetchMock.mockResolvedValueOnce(answer(410, { error: { code: "signup_code_expired", message: "gone" } }));

    const state = await confirmSignupAction({}, form({ email: "ana@example.com", code: "123456" }));

    expect(state).toEqual({ code: "signup_code_expired", values: { email: "ana@example.com" } });
    expect(redirect).not.toHaveBeenCalled();
  });

  test("relays the clearing of a spent sign-up even when the sign-in after it fails", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(503, { error: { code: "sign_in_busy", message: "busy" } }, ["dbcontest_signup=; Path=/; Max-Age=0"]),
    );

    const state = await confirmSignupAction({}, form({ email: "ana@example.com", code: "123456" }));

    expect(state.code).toBe("sign_in_busy");
    expect(jar.set).toHaveBeenCalledWith("dbcontest_signup", "", { ...PROTECTIONS, maxAge: 0 });
  });

  test("says sign-up is not offered when the API has no such route", async () => {
    fetchMock.mockResolvedValueOnce(answer(404, { error: { code: "not_found", message: "Resource not found" } }));

    expect((await confirmSignupAction({}, form({ email: "ana@example.com", code: "123456" }))).code).toBe(
      "signup_not_available",
    );
  });
});

describe("resendCodeAction", () => {
  test("asks for a new code in the reader's language, with the browser's sign-up cookie", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(202, { status: "code_sent", email: "ana@example.com" }, [
        "dbcontest_signup=tok; Path=/; Max-Age=1800; HttpOnly; SameSite=Lax",
      ]),
    );

    expect(await resendCodeAction(" ana@example.com ")).toEqual({ sent: true });

    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toBe("http://api.test/api/v1/public/signup/resend?lang=ru");
    expect(JSON.parse(String(init?.body))).toEqual({ email: "ana@example.com" });
    expect(new Headers(init?.headers).get("cookie")).toBe("dbcontest_signup=tok");
  });

  test("hands the renewed sign-up cookie back to the browser", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(202, { status: "code_sent", email: "ana@example.com" }, [
        "dbcontest_signup=tok; Path=/; Max-Age=1800; HttpOnly; SameSite=Lax",
      ]),
    );

    await resendCodeAction("ana@example.com");

    expect(jar.set).toHaveBeenCalledWith("dbcontest_signup", "tok", { ...PROTECTIONS, maxAge: 1800 });
  });

  test("says how long to wait when a code went out too recently", async () => {
    fetchMock.mockResolvedValueOnce(
      answer(429, { error: { code: "signup_too_soon", message: "soon" } }, [], { "retry-after": "37" }),
    );

    expect(await resendCodeAction("ana@example.com")).toEqual({
      sent: false,
      code: "signup_too_soon",
      retryAfterSeconds: 37,
    });
  });

  test("refuses an empty address without asking the API", async () => {
    expect(await resendCodeAction("  ")).toEqual({ sent: false, code: "invalid_request" });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
