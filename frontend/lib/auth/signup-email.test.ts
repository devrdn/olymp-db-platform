import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const { jar } = vi.hoisted(() => ({ jar: { get: vi.fn(), set: vi.fn(), delete: vi.fn() } }));
vi.mock("next/headers", () => ({ cookies: async () => jar }));

import { forgetSignupEmail, rememberSignupEmail, signupEmail, signupEmailCookieName } from "./signup-email";

beforeEach(() => {
  jar.get.mockReset();
  jar.set.mockReset();
  jar.delete.mockReset();
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("the sign-up address cookie", () => {
  /**
   * `__Host-` would demand Path=/, and a browser drops a `__Host-` cookie
   * scoped to /signup, so the prefix that only demands Secure is the one that
   * fits a cookie kept to the sign-up pages.
   */
  test("is prefixed to demand TLS where the deployment has it", () => {
    vi.stubEnv("COOKIE_SECURE", "true");
    expect(signupEmailCookieName()).toBe("__Secure-dbcontest_signup_email");
  });

  test("is bare without TLS", () => {
    vi.stubEnv("COOKIE_SECURE", "false");
    expect(signupEmailCookieName()).toBe("dbcontest_signup_email");
  });

  test("keeps the address for the life of a code, only for the sign-up pages, out of scripts' reach", async () => {
    vi.stubEnv("COOKIE_SECURE", "true");

    await rememberSignupEmail("ana@example.com");

    expect(jar.set).toHaveBeenCalledWith("__Secure-dbcontest_signup_email", "ana@example.com", {
      path: "/signup",
      maxAge: 30 * 60,
      httpOnly: true,
      sameSite: "lax",
      secure: true,
    });
  });

  test("reads the address back", async () => {
    vi.stubEnv("COOKIE_SECURE", "false");
    jar.get.mockImplementation((name: string) =>
      name === "dbcontest_signup_email" ? { name, value: "ana@example.com" } : undefined,
    );

    expect(await signupEmail()).toBe("ana@example.com");
  });

  test("reads nothing when the browser holds no address", async () => {
    vi.stubEnv("COOKIE_SECURE", "false");
    jar.get.mockReturnValue(undefined);

    expect(await signupEmail()).toBeUndefined();
  });

  /** A forged or stale value is not an address, so it is not read as one. */
  test("reads nothing from a value no sign-up could have written", async () => {
    vi.stubEnv("COOKIE_SECURE", "false");
    jar.get.mockReturnValue({ name: "dbcontest_signup_email", value: "x".repeat(300) });

    expect(await signupEmail()).toBeUndefined();
  });

  test("is cleared on the path it was set for", async () => {
    vi.stubEnv("COOKIE_SECURE", "false");

    await forgetSignupEmail();

    expect(jar.delete).toHaveBeenCalledWith({ name: "dbcontest_signup_email", path: "/signup", secure: false });
  });
});
