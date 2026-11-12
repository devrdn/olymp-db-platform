import { afterEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "./client";
import {
  confirmSignup,
  resendSignup,
  signupAnswerCode,
  signupConfirmedSchema,
  signupCookieName,
  signupSentSchema,
  startSignup,
  type Relay,
} from "./signup";

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("the sign-up answers", () => {
  test("reads the start and resend answer as the address the code went to", () => {
    expect(signupSentSchema.parse({ status: "code_sent", email: "ana@example.com" })).toEqual({
      email: "ana@example.com",
    });
  });

  test("reads the confirmation's answer", () => {
    expect(signupConfirmedSchema.parse({ must_change_password: false })).toEqual({ mustChangePassword: false });
  });

  test("refuses an answer that is not the contract", () => {
    expect(() => signupSentSchema.parse({ status: "sent" })).toThrow();
  });
});

describe("signupCookieName", () => {
  test("is prefixed as the API names it over TLS", () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    expect(signupCookieName()).toBe("__Host-dbcontest_signup");
  });

  test("is bare without TLS", () => {
    vi.stubEnv("COOKIE_SECURE", "false");
    expect(signupCookieName()).toBe("dbcontest_signup");
  });
});

describe("signupAnswerCode", () => {
  /** The routes are not mounted when sign-up is off. */
  test("reads the router's not_found as sign-up not being offered", () => {
    expect(signupAnswerCode(new ApiError("not_found", 404, "Resource not found"))).toBe("signup_not_available");
  });

  test("passes every other code through", () => {
    expect(signupAnswerCode(new ApiError("login_taken", 409, "taken"))).toBe("login_taken");
    expect(signupAnswerCode(new Error("socket"))).toBe("unreachable");
  });
});

function relayTo(response: Response, sent: { url?: string; init?: RequestInit } = {}) {
  const handed: unknown[] = [];
  const relay: Relay = {
    origin: "http://api.test",
    headers: { "x-forwarded-for": "203.0.113.7" },
    cookie: "dbcontest_signup=tok",
    setCookie: (cookie) => handed.push(cookie),
    fetchImpl: async (url, init) => {
      sent.url = String(url);
      sent.init = init;
      return response;
    },
  };
  return { relay, handed };
}

describe("the relayed calls", () => {
  test("start posts the account and hands the sign-up cookie on", async () => {
    const sent: { url?: string; init?: RequestInit } = {};
    const { relay, handed } = relayTo(
      new Response(JSON.stringify({ status: "code_sent", email: "ana@example.com" }), {
        status: 202,
        headers: {
          "content-type": "application/json",
          "set-cookie": "dbcontest_signup=tok; Path=/; Max-Age=1800; HttpOnly; SameSite=Lax",
        },
      }),
      sent,
    );

    const answer = await startSignup(
      { full_name: "Ana Popescu", login: "ana", email: "ana@example.com", password: "a long enough secret" },
      "ro",
      relay,
    );

    expect(answer).toEqual({ email: "ana@example.com" });
    expect(sent.url).toBe("http://api.test/api/v1/public/signup?lang=ro");
    expect(JSON.parse(String(sent.init?.body))).toEqual({
      full_name: "Ana Popescu",
      login: "ana",
      email: "ana@example.com",
      password: "a long enough secret",
    });
    expect(new Headers(sent.init?.headers).get("x-forwarded-for")).toBe("203.0.113.7");
    expect(handed).toEqual([{ name: "dbcontest_signup", value: "tok", path: "/", maxAge: 1800 }]);
  });

  test("confirm forwards the browser's cookies and hands every cookie of the answer on", async () => {
    const sent: { url?: string; init?: RequestInit } = {};
    const headers = new Headers({ "content-type": "application/json" });
    headers.append("set-cookie", "dbcontest_signup=; Path=/; Max-Age=0; HttpOnly");
    headers.append("set-cookie", "dbcontest_session=sess; Path=/; Max-Age=43200; HttpOnly");
    headers.append("set-cookie", "dbcontest_device=dev; Path=/; Max-Age=31536000; HttpOnly");
    const { relay, handed } = relayTo(
      new Response(JSON.stringify({ must_change_password: false }), { status: 200, headers }),
      sent,
    );

    const answer = await confirmSignup({ email: "ana@example.com", code: "123456" }, relay);

    expect(answer).toEqual({ mustChangePassword: false });
    expect(sent.url).toBe("http://api.test/api/v1/public/signup/confirm");
    expect(new Headers(sent.init?.headers).get("cookie")).toBe("dbcontest_signup=tok");
    expect(handed).toEqual([
      { name: "dbcontest_signup", value: "", path: "/", maxAge: 0 },
      { name: "dbcontest_session", value: "sess", path: "/", maxAge: 43200 },
      { name: "dbcontest_device", value: "dev", path: "/", maxAge: 31536000 },
    ]);
  });

  /** A spent sign-up clears its cookie even when the sign-in after it fails. */
  test("hands the answer's cookies on when the API refuses too, and throws its code", async () => {
    const { relay, handed } = relayTo(
      new Response(JSON.stringify({ error: { code: "sign_in_busy", message: "busy" } }), {
        status: 503,
        headers: { "content-type": "application/json", "set-cookie": "dbcontest_signup=; Path=/; Max-Age=0" },
      }),
    );

    await expect(confirmSignup({ email: "ana@example.com", code: "123456" }, relay)).rejects.toMatchObject({
      code: "sign_in_busy",
    });
    expect(handed).toEqual([{ name: "dbcontest_signup", value: "", path: "/", maxAge: 0 }]);
  });

  test("resend asks in the reader's language and keeps how long the API says to wait", async () => {
    const sent: { url?: string; init?: RequestInit } = {};
    const { relay } = relayTo(
      new Response(JSON.stringify({ error: { code: "signup_too_soon", message: "soon" } }), {
        status: 429,
        headers: { "content-type": "application/json", "retry-after": "42" },
      }),
      sent,
    );

    await expect(resendSignup({ email: "ana@example.com" }, "ru", relay)).rejects.toMatchObject({
      code: "signup_too_soon",
      retryAfterSeconds: 42,
    });
    expect(sent.url).toBe("http://api.test/api/v1/public/signup/resend?lang=ru");
    expect(JSON.parse(String(sent.init?.body))).toEqual({ email: "ana@example.com" });
  });
});
